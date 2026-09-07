// Package session manages local conversation history.
package session

import (
	"fmt"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/redact"

	"github.com/google/uuid"
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
	// Agent is the active specialised agent for this session (e.g. "coding").
	// Empty means old sessions that predate agents; callers should treat
	// "" as "general" for backwards compatibility.
	Agent    string    `json:"agent,omitempty"`
	Messages []Message `json:"messages"`
	// Compacted counts messages dropped by size-bound compaction across the
	// session lifetime. It makes the retention policy observable instead of
	// silent: UIs and doctor can report how much history was summarized
	// away, and tests can assert bounded growth without parsing content.
	Compacted int `json:"compacted,omitempty"`
	// Turn is the execution envelope for the latest tool-calling turn:
	// which calls were decided, which are still running, and how the
	// turn ended. It is what makes a crash between "model responded" and
	// "tool results persisted" recoverable instead of ambiguous. Nil for
	// old session files (which predate it) and for sessions that never
	// ran a tool; every field is additive and omitempty.
	Turn *TurnState `json:"turn,omitempty"`
}

// TurnStatus is the lifecycle state of one tool-calling turn.
type TurnStatus string

const (
	// TurnInProgress means tool calls were decided and at least one has
	// not reached a terminal state. A Turn left in this state on disk
	// means Forcefield stopped unexpectedly mid-turn.
	TurnInProgress TurnStatus = "in_progress"
	// TurnComplete means the run finished the turn normally.
	TurnComplete TurnStatus = "complete"
	// TurnInterrupted means the process died mid-turn (crash/kill): the
	// calls were never re-executed and must stay terminal.
	TurnInterrupted TurnStatus = "interrupted"
	// TurnCancelled means the user (or a teardown path) cancelled the
	// turn before it finished.
	TurnCancelled TurnStatus = "cancelled"
)

// CallStatus is the execution state of one tool call within a turn.
// in_progress (pending/running) is the only non-terminal pair; every
// other value is final and never re-queued.
type CallStatus string

const (
	CallPending     CallStatus = "pending"
	CallRunning     CallStatus = "running"
	CallDone        CallStatus = "done"
	CallFailed      CallStatus = "failed"
	CallDenied      CallStatus = "denied"
	CallCancelled   CallStatus = "cancelled"
	CallInterrupted CallStatus = "interrupted"
)

// isTerminalCallStatus reports whether st ends a call's lifecycle.
func isTerminalCallStatus(st CallStatus) bool {
	switch st {
	case CallDone, CallFailed, CallDenied, CallCancelled, CallInterrupted:
		return true
	default:
		return false
	}
}

// PendingCall tracks one tool call's execution status within the active
// turn. ID/Name/Arguments mirror the assistant tool_calls batch so
// recovery can pair intent with outcome without re-running anything.
type PendingCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Status    CallStatus     `json:"status"`
	StartedAt time.Time      `json:"started_at,omitempty"`
	EndedAt   time.Time      `json:"ended_at,omitempty"`
	Attempt   int            `json:"attempt,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// TurnState is the crash-recovery envelope for the latest tool-calling
// turn. It holds at most one batch (the run loop finishes each batch
// before the next model turn), so it never grows with history.
type TurnState struct {
	ID string `json:"id,omitempty"`
	// Status is the turn lifecycle state; in_progress on disk means the
	// process stopped before the turn finished.
	Status TurnStatus `json:"status,omitempty"`
	// ModelCompleted reports whether the model response that decided
	// this turn finished streaming (true once tool calls were produced
	// or the final answer completed). Informational for debugging
	// failed runs; recovery never branches on it.
	ModelCompleted bool          `json:"model_completed,omitempty"`
	StartedAt      time.Time     `json:"started_at,omitempty"`
	EndedAt        time.Time     `json:"ended_at,omitempty"`
	Pending        []PendingCall `json:"pending,omitempty"`
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
	// maxSessionMessages-2, reserving one slot for an observable compaction
	// marker so dropped history is explicit, not silent. The provider
	// window (100) is smaller; this is the persistence bound.
	keepFirst := 0
	if len(s.Messages) > 0 && s.Messages[0].Role == "user" {
		keepFirst = 1
	}
	keepRecent := maxSessionMessages - keepFirst - 1
	if keepRecent < 0 {
		keepRecent = 0
	}
	recentStart := len(s.Messages) - keepRecent
	if recentStart < keepFirst {
		recentStart = keepFirst
	}
	dropped := recentStart - keepFirst
	newMessages := make([]Message, 0, maxSessionMessages)
	if keepFirst == 1 {
		newMessages = append(newMessages, s.Messages[0])
	}
	marker := Message{
		Role:    "system",
		Content: fmt.Sprintf("[compacted %d older messages to bound session size; use /sessions to review recent history]", dropped),
		Time:    time.Now(),
	}
	newMessages = append(newMessages, marker)
	newMessages = append(newMessages, s.Messages[recentStart:]...)
	s.Messages = newMessages
	s.Compacted += dropped
}

// ProviderMessages converts session history to provider messages.
// Tool results are fenced so the model treats them as data, not
// instructions (prompt-injection mitigation). System compaction markers
// are persistence observability only and are skipped here; the provider
// sliding window already bounds what the model sees.
func (s *Session) ProviderMessages() []providers.Message {
	messages := make([]providers.Message, 0, len(s.Messages))

	for _, msg := range s.Messages {
		if msg.Role == "system" {
			continue
		}
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
	// sensitive files now require Ask. Tool-call arguments get the same
	// treatment (copied — the caller's map is never mutated): commands,
	// paths, and file content routinely carry credentials.
	msg.Content = ScrubContent(msg.Content)
	if len(msg.ToolCalls) > 0 {
		calls := make([]providers.ToolCall, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			tc.Arguments = redact.ScrubMap(tc.Arguments)
			calls[i] = tc
		}
		msg.ToolCalls = calls
	}
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

// RepairInterruptedTurn appends synthetic cancelled results for assistant
// tool calls that have no matching tool result message. Cancellation (or
// a crash/kill) can strand an assistant tool_calls batch: the batch is
// persisted when the call starts, but the result never arrives because
// the run was torn down first. Left alone, the session would replay an
// assistant message with dangling tool calls, which strict provider APIs
// reject — making the session unresumable.
//
// Repair keeps the session recoverable: every tool call gains a result,
// the user can continue or resume, and the file stays a faithful record
// (the content names the interruption explicitly). It returns how many
// results were synthesized; 0 means the session was already consistent.
// Callers should Save when the count is positive.
func (s *Session) RepairInterruptedTurn() int {
	if s == nil {
		return 0
	}
	resultIDs := make(map[string]struct{}, len(s.Messages))
	for _, m := range s.Messages {
		if m.Role == string(providers.ToolRole) && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = struct{}{}
		}
	}
	type orphan struct {
		id   string
		name string
	}
	var orphans []orphan
	for _, m := range s.Messages {
		if m.Role != string(providers.AssistantRole) {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == "" {
				continue
			}
			if _, ok := resultIDs[tc.ID]; ok {
				continue
			}
			orphans = append(orphans, orphan{id: tc.ID, name: tc.Name})
			resultIDs[tc.ID] = struct{}{} // same ID twice: repair once
		}
	}
	for _, o := range orphans {
		s.AddToolResult(o.id, o.name, "tool execution cancelled (turn interrupted before the result was recorded)")
	}
	return len(orphans)
}

// BeginTurn starts a new tool-calling turn envelope, replacing any
// finished turn's record. The run loop finishes each tool batch before
// the next model turn, so at most one turn is ever in flight and the
// envelope stays bounded. It returns the new turn ID.
func (s *Session) BeginTurn() string {
	if s == nil {
		return ""
	}
	id := uuid.NewString()
	now := time.Now()
	s.Turn = &TurnState{
		ID:        id,
		Status:    TurnInProgress,
		StartedAt: now,
	}
	s.UpdatedAt = now
	return id
}

// ensureTurn returns the active in-progress turn, starting one when
// there is none or the last one already reached a terminal state.
func (s *Session) ensureTurn() *TurnState {
	if s == nil {
		return nil
	}
	if s.Turn == nil || s.Turn.Status != TurnInProgress {
		s.BeginTurn()
	}
	return s.Turn
}

// AddPendingCall records a decided tool call as running before its
// execution completes. Call it alongside the assistant tool_calls batch
// persist, under the same Save, so intent and status commit atomically.
// A duplicate ID is ignored: the first record wins. String arguments are
// scrubbed (a copied map — the caller's is never mutated) so secrets in
// commands, paths, or content never reach the session file.
func (s *Session) AddPendingCall(call providers.ToolCall) {
	if s == nil || call.ID == "" {
		return
	}
	turn := s.ensureTurn()
	if turn == nil {
		return
	}
	for _, p := range turn.Pending {
		if p.ID == call.ID {
			return
		}
	}
	now := time.Now()
	turn.Pending = append(turn.Pending, PendingCall{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: redact.ScrubMap(call.Arguments),
		Status:    CallRunning,
		StartedAt: now,
	})
	turn.ModelCompleted = true // calls exist only after a full response
	s.UpdatedAt = now
}

// ResolvePendingCall marks a pending call terminal. Unknown IDs are
// ignored so late or duplicate terminal events can never corrupt the
// record. Terminal states are final: resolving twice keeps the first.
func (s *Session) ResolvePendingCall(id string, status CallStatus, errMsg string) {
	if s == nil || s.Turn == nil || id == "" {
		return
	}
	if !isTerminalCallStatus(status) {
		return
	}
	for i := range s.Turn.Pending {
		p := &s.Turn.Pending[i]
		if p.ID != id || isTerminalCallStatus(p.Status) {
			continue
		}
		p.Status = status
		p.EndedAt = time.Now()
		p.Error = redact.Scrub(errMsg)
		s.UpdatedAt = p.EndedAt
		return
	}
}

// EndTurn closes the active turn with a terminal status. It only
// transitions out of in_progress, so a late Done can never overwrite an
// already-recorded cancel or interrupt. Unsettled calls settle with the
// turn (cancelled turns cancel them, interrupted ones interrupt them; a
// complete turn with a still-running call marks it interrupted, since its
// outcome was never observed). It returns true when it changed anything;
// callers should Save then.
func (s *Session) EndTurn(status TurnStatus, modelCompleted bool) bool {
	if s == nil || s.Turn == nil {
		return false
	}
	if s.Turn.Status != TurnInProgress {
		return false
	}
	switch status {
	case TurnComplete, TurnInterrupted, TurnCancelled:
		s.Turn.Status = status
	default:
		return false
	}
	now := time.Now()
	s.Turn.EndedAt = now
	if modelCompleted {
		s.Turn.ModelCompleted = modelCompleted
	}
	for i := range s.Turn.Pending {
		p := &s.Turn.Pending[i]
		if isTerminalCallStatus(p.Status) {
			continue
		}
		if status == TurnCancelled {
			p.Status = CallCancelled
		} else {
			p.Status = CallInterrupted
		}
		p.EndedAt = now
	}
	s.UpdatedAt = now
	return true
}

// CancelTurn marks the active turn and its unsettled calls cancelled.
// It is idempotent and a no-op without an in-progress turn; callers
// should Save when it reports a change.
func (s *Session) CancelTurn() bool {
	if s == nil || s.Turn == nil || s.Turn.Status != TurnInProgress {
		return false
	}
	now := time.Now()
	s.Turn.Status = TurnCancelled
	s.Turn.EndedAt = now
	for i := range s.Turn.Pending {
		p := &s.Turn.Pending[i]
		if !isTerminalCallStatus(p.Status) {
			p.Status = CallCancelled
			p.EndedAt = now
		}
	}
	s.UpdatedAt = now
	return true
}

// RecoverInterruptedTurn detects a turn that was in progress when
// Forcefield stopped unexpectedly (crash/kill) and marks it and its
// unsettled calls interrupted. Interrupted calls are terminal: recovery
// never re-executes anything, it only records. Pair with
// RepairInterruptedTurn (which synthesizes the missing tool result
// messages) and Save when it reports a change.
func (s *Session) RecoverInterruptedTurn() bool {
	if s == nil || s.Turn == nil || s.Turn.Status != TurnInProgress {
		return false
	}
	now := time.Now()
	s.Turn.Status = TurnInterrupted
	s.Turn.EndedAt = now
	for i := range s.Turn.Pending {
		p := &s.Turn.Pending[i]
		if !isTerminalCallStatus(p.Status) {
			p.Status = CallInterrupted
			p.EndedAt = now
		}
	}
	s.UpdatedAt = now
	return true
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
