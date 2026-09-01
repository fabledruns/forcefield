// Package session manages local conversation history.
package session

import (
	"forcefield/internal/providers"
	"time"
)

// Message is one session message. ToolCalls, ToolCallID, and Name are
// stored with omitempty so old session files containing only Role/Content/Time
// continue to load. ProviderMessages restores them when present.
type Message struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	Time    time.Time `json:"time"`

	ToolCalls []providers.ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID and Name are used for tool result messages (role=="tool").
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// Session is a persisted conversation.
type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Messages []Message `json:"messages"`
}

// maxSessionMessages bounds how many messages a session file may hold.
// Beyond this, oldest messages (except the initial goal) are dropped so
// the file never grows to 10s of MB over a 5-day run. The provider sliding
// window (100) is smaller; this is the persistence bound.
const maxSessionMessages = 1000

func (s *Session) compactIfNeeded() {
	if len(s.Messages) <= maxSessionMessages {
		return
	}
	// Keep the first message (often the user's goal) plus the most recent
	// maxSessionMessages-1. This preserves intent and recent history while
	// bounding file size. A marker is not inserted to keep JSON simple;
	// the provider window already handles truncation.
	keepFirst := 0
	if len(s.Messages) > 0 && s.Messages[0].Role == "user" {
		keepFirst = 1
	}
	// Number of recent messages to keep
	keepRecent := maxSessionMessages - keepFirst
	if keepRecent < 0 {
		keepRecent = 0
	}
	recentStart := len(s.Messages) - keepRecent
	if recentStart < keepFirst {
		recentStart = keepFirst
	}
	newMessages := make([]Message, 0, maxSessionMessages)
	if keepFirst == 1 {
		newMessages = append(newMessages, s.Messages[0])
	}
	newMessages = append(newMessages, s.Messages[recentStart:]...)
	s.Messages = newMessages
}

// ProviderMessages converts session history to provider messages.
// Tool results are fenced so the model treats them as data, not
// instructions (prompt-injection mitigation).
func (s *Session) ProviderMessages() []providers.Message {
	messages := make([]providers.Message, 0, len(s.Messages))

	for _, msg := range s.Messages {
		content := msg.Content
		if msg.Role == string(providers.ToolRole) {
			content = FenceToolResult(msg.Name, content)
		}
		messages = append(messages, providers.Message{
			Role:       providers.Role(msg.Role),
			Content:    content,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		})
	}

	return messages
}

// AddProviderMessage appends a full provider message with fidelity, storing
// tool fields when present. Prefer this for assistant tool_calls and tool
// results; AddMessage remains for simple user/assistant text.
func (s *Session) AddProviderMessage(msg providers.Message) {
	// Scrub secrets before persistence so session files and provider replay
	// never contain raw keys. This is defense-in-depth even though
	// sensitive files now require Ask.
	msg.Content = ScrubContent(msg.Content)
	s.Messages = append(s.Messages, Message{
		Role:       string(msg.Role),
		Content:    msg.Content,
		Time:       time.Now(),
		ToolCalls:  msg.ToolCalls,
		ToolCallID: msg.ToolCallID,
		Name:       msg.Name,
	})
	s.UpdatedAt = time.Now()
	s.compactIfNeeded()
}

// AddAssistantToolCalls appends an assistant message that contains tool calls
// (and optional accompanying text). It is the session-persistence side of the
// in-memory messages = append(assistant, ToolCalls) in runtime.run.
func (s *Session) AddAssistantToolCalls(content string, toolCalls []providers.ToolCall) {
	s.AddProviderMessage(providers.Message{
		Role:      providers.AssistantRole,
		Content:   content,
		ToolCalls: toolCalls,
	})
}

// AddToolResult appends a tool result message (role=="tool") linked to a
// previous assistant tool call via ToolCallID.
func (s *Session) AddToolResult(toolCallID, name, content string) {
	s.AddProviderMessage(providers.Message{
		Role:       providers.ToolRole,
		ToolCallID: toolCallID,
		Name:       name,
		Content:    content,
	})
}

// AppendToolCallToLastAssistant appends one ToolCall to the last assistant
// message's ToolCalls when that message is the current turn's assistant
// tool_calls batch. If the last message is not an assistant with existing
// tool calls, it creates a new assistant message containing just this call.
// This allows the TUI to persist incremental EventToolStart events as a
// single batched assistant message per turn.
//
// Turn ownership is enforced by a time window: only messages from the last
// few seconds are considered part of the same turn. Without this, two
// separate turns that happen to be consecutive assistant tool batches (e.g.
// after a resume where the last persisted message is an old assistant batch)
// would be incorrectly coalesced, corrupting the batch.
func (s *Session) AppendToolCallToLastAssistant(call providers.ToolCall, content string) {
	content = ScrubContent(content)
	if n := len(s.Messages); n > 0 {
		last := &s.Messages[n-1]
		if last.Role == string(providers.AssistantRole) && len(last.ToolCalls) > 0 {
			// Same-turn check: last message must be recent and no tool result
			// has been interleaved (last.Role is still assistant, not tool).
			// The 10s window is generous for concurrent scheduler starts but
			// prevents cross-turn coalescing after a resume or long pause.
			if time.Since(last.Time) < 10*time.Second {
				// Deduplicate by ID in case the same call is appended twice
				for _, existing := range last.ToolCalls {
					if existing.ID == call.ID {
						return
					}
				}
				last.ToolCalls = append(last.ToolCalls, call)
				if content != "" && last.Content == "" {
					last.Content = content
				}
				s.UpdatedAt = time.Now()
				return
			}
		}
	}
	s.AddAssistantToolCalls(content, []providers.ToolCall{call})
}
