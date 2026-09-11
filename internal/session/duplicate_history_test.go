package session

import (
	"testing"

	"forcefield/internal/providers"
)

func TestDuplicateAssistantToolCallsPersistOnce(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("checking", []providers.ToolCall{
		{ID: "dup-1", Name: "shell", Arguments: map[string]any{"command": "echo hi"}},
	})
	s.AddAssistantToolCalls("checking again", []providers.ToolCall{
		{ID: "dup-1", Name: "shell", Arguments: map[string]any{"command": "echo hi"}},
	})
	if len(s.Messages) != 1 {
		t.Fatalf("messages = %d, want 1 (duplicate assistant batch skipped)", len(s.Messages))
	}
	if len(s.Messages[0].ToolCalls) != 1 || s.Messages[0].ToolCalls[0].ID != "dup-1" {
		t.Fatalf("ToolCalls = %+v, want single dup-1", s.Messages[0].ToolCalls)
	}
}

func TestDuplicateToolResultsPersistOnce(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{{ID: "dup-1", Name: "shell"}})
	s.AddToolResult("dup-1", "shell", "first")
	s.AddToolResult("dup-1", "shell", "second")
	if len(s.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (duplicate result skipped)", len(s.Messages))
	}
	if s.Messages[1].Content != "first" {
		t.Errorf("result = %q, want first (second ignored)", s.Messages[1].Content)
	}
	msgs := s.ProviderMessages()
	assistants, results := 0, 0
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID == "dup-1" {
				assistants++
			}
		}
		if string(m.Role) == string(providers.ToolRole) && m.ToolCallID == "dup-1" {
			results++
		}
	}
	if assistants != 1 || results != 1 {
		t.Errorf("replay has %d assistant + %d results for dup-1, want 1+1", assistants, results)
	}
}

func TestUniqueToolCallsUnchanged(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: "a-1", Name: "shell"},
		{ID: "a-2", Name: "shell"},
	})
	s.AddToolResult("a-1", "shell", "one")
	s.AddToolResult("a-2", "shell", "two")
	if len(s.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (unique calls untouched)", len(s.Messages))
	}
}

func TestAppendToolCallGlobalDedupAcrossBatches(t *testing.T) {
	s := New()
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "x-1", Name: "shell"}, "")
	s.AddToolResult("x-1", "shell", "out")
	// A later batch echoing x-1 must not create a second assistant pair.
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "x-1", Name: "shell"}, "")
	count := 0
	for _, m := range s.Messages {
		for _, tc := range m.ToolCalls {
			if tc.ID == "x-1" {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("assistant pairs for x-1 = %d, want 1", count)
	}
}

func TestDuplicateMixedBatchKeepsOnlyNew(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{{ID: "old-1", Name: "shell"}})
	s.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: "old-1", Name: "shell"},
		{ID: "new-1", Name: "shell"},
	})
	if len(s.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (mixed batch keeps only new)", len(s.Messages))
	}
	got := s.Messages[1].ToolCalls
	if len(got) != 1 || got[0].ID != "new-1" {
		t.Fatalf("second batch = %+v, want only new-1", got)
	}
}
