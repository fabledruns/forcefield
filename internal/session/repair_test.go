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

// An orphaned call without an ID cannot be paired by ID, so repair
// drops it from its batch (with an explicit note when the batch would
// otherwise become an empty shell) instead of leaving a dangling call.
func TestRepairInterruptedTurn_DropsEmptyIDOrphan(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{{ID: "", Name: "shell"}})

	if n := s.RepairInterruptedTurn(); n != 1 {
		t.Fatalf("RepairInterruptedTurn() = %d, want 1 (one empty-ID orphan dropped)", n)
	}
	if len(s.Messages) != 1 {
		t.Fatalf("messages = %d, want 1 (batch kept as an explicit note)", len(s.Messages))
	}
	if len(s.Messages[0].ToolCalls) != 0 {
		t.Fatalf("ToolCalls = %+v, want the orphan dropped", s.Messages[0].ToolCalls)
	}
	if !strings.Contains(s.Messages[0].Content, "empty tool call dropped") {
		t.Errorf("content = %q, want the explicit drop note", s.Messages[0].Content)
	}
	assertNoDanglingCalls(t, s)
	// Repair is idempotent: the note is not an orphan, so a second pass
	// finds nothing.
	if n := s.RepairInterruptedTurn(); n != 0 {
		t.Errorf("second RepairInterruptedTurn() = %d, want 0", n)
	}
}

// A completed empty-ID call (result already present) is paired
// positionally and survives repair untouched.
func TestRepairInterruptedTurn_KeepsPairedEmptyIDCall(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{{ID: "", Name: "shell"}})
	s.AddToolResult("", "shell", "output")

	if n := s.RepairInterruptedTurn(); n != 0 {
		t.Fatalf("RepairInterruptedTurn() = %d, want 0 (empty-ID call already has its result)", n)
	}
	if len(s.Messages) != 2 || len(s.Messages[0].ToolCalls) != 1 {
		t.Fatalf("paired empty-ID batch disturbed: %+v", s.Messages)
	}
	assertNoDanglingCalls(t, s)
}

// Mixed batches keep paired and ID-linked calls while dropping only
// the genuinely orphaned empty-ID call.
func TestRepairInterruptedTurn_MixedEmptyAndNamedOrphans(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("checking", []providers.ToolCall{
		{ID: "k1", Name: "shell"},
		{ID: "", Name: "shell"},
	})
	s.AddToolResult("k1", "shell", "done")

	if n := s.RepairInterruptedTurn(); n != 1 {
		t.Fatalf("RepairInterruptedTurn() = %d, want 1 (only the empty-ID orphan dropped)", n)
	}
	got := s.Messages[0].ToolCalls
	if len(got) != 1 || got[0].ID != "k1" {
		t.Fatalf("batch = %+v, want only k1", got)
	}
	if s.Messages[0].Content != "checking" {
		t.Errorf("accompanying text changed: %q", s.Messages[0].Content)
	}
	assertNoDanglingCalls(t, s)
}

// assertNoDanglingCalls simulates provider replay validation: every
// assistant tool call (including empty-ID ones) must resolve against a
// recorded result.
func assertNoDanglingCalls(t *testing.T, s *Session) {
	t.Helper()
	type key struct {
		id  string
		idx int
	}
	pending := map[key]bool{}
	emptyResults := 0
	for _, m := range s.ProviderMessages() {
		if m.Role == providers.AssistantRole {
			for i, tc := range m.ToolCalls {
				pending[key{tc.ID, i}] = true
			}
		}
		if m.Role == providers.ToolRole {
			if m.ToolCallID != "" {
				for k := range pending {
					if k.id == m.ToolCallID {
						delete(pending, k)
					}
				}
			} else {
				emptyResults++
			}
		}
	}
	for k := range pending {
		if k.id == "" {
			if emptyResults > 0 {
				emptyResults--
				delete(pending, k)
			}
		}
	}
	if len(pending) != 0 {
		t.Errorf("dangling calls after repair: %v", pending)
	}
}
