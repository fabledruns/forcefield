package tui

import (
	"context"
	"errors"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// feedEvent delivers one runtime event through Update with a matching
// generation, returning the updated model.
func feedEvent(t *testing.T, m model, ev runtime.Event) model {
	t.Helper()
	next, _ := m.Update(streamEventMsg{Event: ev, gen: m.streamGen})
	got, ok := next.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", next)
	}
	return got
}

func startTestStream(t *testing.T) model {
	t.Helper()
	isolateModelHome(t)
	m := newTestModel()
	m.session = session.New()
	m.streamGen = 1
	m.waiting = true
	m.activeTools = make(map[string]int)
	return m
}

func pendingStatus(m model, id string) session.CallStatus {
	if m.session.Turn == nil {
		return ""
	}
	for _, p := range m.session.Turn.Pending {
		if p.ID == id {
			return p.Status
		}
	}
	return "missing"
}

// TestToolStartTracksPendingCall pins that the first scheduler event for
// a call persists both the replay batch and the running pending record
// under one Save.
func TestToolStartTracksPendingCall(t *testing.T) {
	m := startTestStream(t)
	m = feedEvent(t, m, runtime.Event{
		Type:     runtime.EventToolStart,
		ToolCall: &providers.ToolCall{ID: "c1", Name: "shell"},
	})

	if m.session.Turn == nil || m.session.Turn.Status != session.TurnInProgress {
		t.Fatalf("Turn = %+v, want in_progress", m.session.Turn)
	}
	if got := pendingStatus(m, "c1"); got != session.CallRunning {
		t.Errorf("c1 status = %q, want running", got)
	}
	foundBatch := false
	for _, msg := range m.session.Messages {
		for _, tc := range msg.ToolCalls {
			if tc.ID == "c1" {
				foundBatch = true
			}
		}
	}
	if !foundBatch {
		t.Error("assistant tool_calls batch missing c1")
	}
}

// TestToolTerminalResolvesPending pins per-outcome statuses and that the
// result message persists alongside the terminal mark.
func TestToolTerminalResolvesPending(t *testing.T) {
	cases := []struct {
		name       string
		event      runtime.EventType
		wantStatus session.CallStatus
	}{
		{"finish", runtime.EventToolFinish, session.CallDone},
		{"failed", runtime.EventToolFailed, session.CallFailed},
		{"cancelled", runtime.EventToolCancelled, session.CallCancelled},
		{"denied", runtime.EventToolDenied, session.CallDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := startTestStream(t)
			m = feedEvent(t, m, runtime.Event{
				Type:     runtime.EventToolStart,
				ToolCall: &providers.ToolCall{ID: "c1", Name: "shell"},
			})
			m = feedEvent(t, m, runtime.Event{
				Type: tc.event,
				ToolResult: &runtime.ToolResult{
					ToolCallID: "c1", Name: "shell",
					Content: "out", Success: tc.event == runtime.EventToolFinish,
				},
			})
			if got := pendingStatus(m, "c1"); got != tc.wantStatus {
				t.Errorf("c1 status = %q, want %q", got, tc.wantStatus)
			}
			foundResult := false
			for _, msg := range m.session.Messages {
				if msg.Role == "tool" && msg.ToolCallID == "c1" {
					foundResult = true
				}
			}
			if !foundResult {
				t.Error("tool result message missing for c1")
			}
		})
	}
}

// TestStreamDoneCompletesTurn pins that a normal finish closes the turn,
// so a later crash is never misattributed to it.
func TestStreamDoneCompletesTurn(t *testing.T) {
	m := startTestStream(t)
	m = feedEvent(t, m, runtime.Event{
		Type:     runtime.EventToolStart,
		ToolCall: &providers.ToolCall{ID: "c1", Name: "shell"},
	})
	m = feedEvent(t, m, runtime.Event{
		Type:       runtime.EventToolFinish,
		ToolResult: &runtime.ToolResult{ToolCallID: "c1", Name: "shell", Content: "ok", Success: true},
	})

	next, _ := m.Update(streamDoneMsg{gen: m.streamGen})
	m = next.(model)

	if m.session.Turn == nil || m.session.Turn.Status != session.TurnComplete {
		t.Fatalf("Turn = %+v, want complete", m.session.Turn)
	}
}

// TestStreamErrorEndsTurn pins cancelled-vs-interrupted recording: a
// context cancellation stays cancelled, any other failure is an
// interruption. Both are terminal and never re-executed.
func TestStreamErrorEndsTurn(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want session.TurnStatus
	}{
		{"cancel", context.Canceled, session.TurnCancelled},
		{"provider failure", errors.New("connection reset"), session.TurnInterrupted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := startTestStream(t)
			m = feedEvent(t, m, runtime.Event{
				Type:     runtime.EventToolStart,
				ToolCall: &providers.ToolCall{ID: "c1", Name: "shell"},
			})

			next, _ := m.Update(streamErrMsg{err: tc.err, gen: m.streamGen})
			m = next.(model)

			if m.session.Turn == nil || m.session.Turn.Status != tc.want {
				t.Fatalf("Turn = %+v, want %q", m.session.Turn, tc.want)
			}
			if got := pendingStatus(m, "c1"); got != session.CallCancelled && got != session.CallInterrupted {
				t.Errorf("c1 status = %q, want a terminal cancel/interrupt", got)
			}
		})
	}
}

// TestSwitchToSessionRecoversInterruptedTurn pins crash recovery on
// adopt: a session whose turn was in flight when the process died loads
// as interrupted with paired replay, and is never re-executed.
func TestSwitchToSessionRecoversInterruptedTurn(t *testing.T) {
	isolateModelHome(t)

	dead := session.New()
	dead.Agent = "general"
	dead.AddMessage("user", "do things")
	dead.AddAssistantToolCalls("", []providers.ToolCall{{ID: "c1", Name: "shell"}})
	dead.AddPendingCall(providers.ToolCall{ID: "c1", Name: "shell"})
	if err := dead.Save(); err != nil {
		t.Fatalf("Save crashed session: %v", err)
	}

	m := newTestModel()
	m.session = session.New()
	m.streamGen = 1

	next, _ := m.switchToSession(dead.ID)
	m = next.(model)

	if m.session.Turn == nil || m.session.Turn.Status != session.TurnInterrupted {
		t.Fatalf("adopted Turn = %+v, want interrupted", m.session.Turn)
	}
	if got := pendingStatus(m, "c1"); got != session.CallInterrupted {
		t.Errorf("c1 status = %q, want interrupted", got)
	}
	// Replay must pair every call after recovery.
	pending := map[string]bool{}
	for _, msg := range m.session.ProviderMessages() {
		if msg.Role == providers.AssistantRole {
			for _, tc := range msg.ToolCalls {
				pending[tc.ID] = true
			}
		}
		if msg.Role == providers.ToolRole && msg.ToolCallID != "" {
			delete(pending, msg.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Errorf("dangling calls after adopt recovery: %v", pending)
	}
}
