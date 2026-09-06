package tui

import (
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/session"
)

// TestStopStreamRepairsStrandedToolCalls pins the core P0.2 session
// guarantee: when a stream is torn down after a ToolStart whose terminal
// event never arrived (dropped by the generation bump, or the worker was
// still settling), the session must not keep a dangling tool_calls batch.
// Repair synthesizes cancelled results so the next replay pairs every
// call with a result and the user can continue or resume.
func TestStopStreamRepairsStrandedToolCalls(t *testing.T) {
	m := newTestModel()
	sess := session.New()
	sess.AddMessage("user", "do things")
	sess.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: "c1", Name: "shell"},
		{ID: "c2", Name: "read_file"},
	})
	sess.AddToolResult("c1", "shell", "done")
	m.session = sess

	m.stopStream(true)

	results := map[string]bool{}
	for _, msg := range m.session.Messages {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			results[msg.ToolCallID] = true
		}
	}
	if !results["c1"] || !results["c2"] {
		t.Errorf("after stopStream, results = %v, want both c1 and c2 paired", results)
	}
	// The transcript activity for the finished call is untouched; only
	// the stranded call gained a persisted result.
	if len(m.activeTools) != 0 {
		t.Errorf("activeTools = %v, want empty after stopStream", m.activeTools)
	}
}

// TestStopStreamKeepsConsistentSessionsUnchanged pins that repair is a
// no-op (no extra messages, no extra saves) when nothing is stranded,
// e.g. on a normal streamDone.
func TestStopStreamKeepsConsistentSessionsUnchanged(t *testing.T) {
	m := newTestModel()
	sess := session.New()
	sess.AddMessage("user", "hi")
	sess.AddAssistantToolCalls("", []providers.ToolCall{{ID: "c1", Name: "pwd"}})
	sess.AddToolResult("c1", "pwd", "/tmp")
	m.session = sess
	before := len(sess.Messages)

	m.stopStream(true)

	if len(m.session.Messages) != before {
		t.Errorf("consistent session grew from %d to %d messages on stopStream", before, len(m.session.Messages))
	}
}

// TestHealSessionNilSafe pins the nil guard used by tests and teardown
// paths that run without an adopted session.
func TestHealSessionNilSafe(t *testing.T) {
	healSession(nil) // must not panic
	m := newTestModel()
	m.session = nil
	m.stopStream(false) // must not panic
}
