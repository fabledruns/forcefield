package session

import (
	"strings"
	"testing"

	"forcefield/internal/providers"
)

func TestRepairInterruptedTurn_AppendsCancelledResults(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: "c1", Name: "shell"},
		{ID: "c2", Name: "read_file"},
	})
	s.AddToolResult("c1", "shell", "done")

	if n := s.RepairInterruptedTurn(); n != 1 {
		t.Fatalf("RepairInterruptedTurn() = %d, want 1 (only c2 orphaned)", n)
	}
	// Every assistant call must now have a matching tool result.
	results := map[string]string{}
	for _, m := range s.Messages {
		if m.Role == string(providers.ToolRole) {
			results[m.ToolCallID] = m.Content
		}
	}
	for _, id := range []string{"c1", "c2"} {
		if _, ok := results[id]; !ok {
			t.Errorf("no tool result for call %q after repair", id)
		}
	}
	if got := results["c2"]; got == "" || !strings.Contains(got, "cancelled") {
		t.Errorf("synthetic result = %q, want it to name the cancellation", got)
	}
	// Repair is idempotent: a second pass finds nothing.
	if n := s.RepairInterruptedTurn(); n != 0 {
		t.Errorf("second RepairInterruptedTurn() = %d, want 0", n)
	}
}

func TestRepairInterruptedTurn_NoOpWhenConsistent(t *testing.T) {
	s := New()
	s.AddMessage("user", "hi")
	s.AddAssistantToolCalls("thinking", []providers.ToolCall{{ID: "c1", Name: "pwd"}})
	s.AddToolResult("c1", "pwd", "/tmp")
	if n := s.RepairInterruptedTurn(); n != 0 {
		t.Errorf("RepairInterruptedTurn() = %d, want 0 for a consistent session", n)
	}
	if len(s.Messages) != 3 {
		t.Errorf("consistent session gained messages: %d, want 3", len(s.Messages))
	}
}

func TestRepairInterruptedTurn_NilSafe(t *testing.T) {
	var s *Session
	if n := s.RepairInterruptedTurn(); n != 0 {
		t.Errorf("nil RepairInterruptedTurn() = %d, want 0", n)
	}
}

func TestRepairInterruptedTurn_ReplayStaysPaired(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{{ID: "c9", Name: "shell"}})
	s.RepairInterruptedTurn()
	for _, m := range s.ProviderMessages() {
		_ = m
	}
	// Simulate provider replay validation: each assistant tool_call ID
	// must have a tool result later in the sequence.
	pending := map[string]bool{}
	for _, m := range s.ProviderMessages() {
		if m.Role == providers.AssistantRole {
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = true
			}
		}
		if m.Role == providers.ToolRole && m.ToolCallID != "" {
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Errorf("dangling calls after repair: %v", pending)
	}
}
