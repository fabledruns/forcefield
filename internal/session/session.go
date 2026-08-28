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

// ProviderMessages converts session history to provider messages.
func (s *Session) ProviderMessages() []providers.Message {
	messages := make([]providers.Message, 0, len(s.Messages))

	for _, msg := range s.Messages {
		messages = append(messages, providers.Message{
			Role:       providers.Role(msg.Role),
			Content:    msg.Content,
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
	s.Messages = append(s.Messages, Message{
		Role:       string(msg.Role),
		Content:    msg.Content,
		Time:       time.Now(),
		ToolCalls:  msg.ToolCalls,
		ToolCallID: msg.ToolCallID,
		Name:       msg.Name,
	})
	s.UpdatedAt = time.Now()
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
func (s *Session) AppendToolCallToLastAssistant(call providers.ToolCall, content string) {
	if n := len(s.Messages); n > 0 {
		last := &s.Messages[n-1]
		if last.Role == string(providers.AssistantRole) && len(last.ToolCalls) > 0 {
			// Still the same turn's batch if updated very recently (within
			// a few seconds) and no tool results have been interleaved.
			last.ToolCalls = append(last.ToolCalls, call)
			if content != "" && last.Content == "" {
				last.Content = content
			}
			s.UpdatedAt = time.Now()
			return
		}
	}
	s.AddAssistantToolCalls(content, []providers.ToolCall{call})
}
