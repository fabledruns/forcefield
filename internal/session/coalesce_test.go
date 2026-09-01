package session

import (
	"testing"
	"time"

	"forcefield/internal/providers"
)

func TestAppendToolCall_CoalescesWithinWindow(t *testing.T) {
	s := New()
	s.AddMessage("user", "goal")
	// First tool call creates new assistant message
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "c1", Name: "shell"}, "")
	if len(s.Messages) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d", len(s.Messages))
	}
	// Second call within window should coalesce
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "c2", Name: "shell"}, "")
	if len(s.Messages) != 2 {
		t.Fatalf("expected coalesced into 1 assistant message, got %d", len(s.Messages))
	}
	if len(s.Messages[1].ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls coalesced, got %d", len(s.Messages[1].ToolCalls))
	}
}

func TestAppendToolCall_DoesNotCoalesceAcrossTime(t *testing.T) {
	s := New()
	s.AddMessage("user", "goal")
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "c1", Name: "shell"}, "")
	// Make last message old
	s.Messages[1].Time = time.Now().Add(-11 * time.Second)
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "c2", Name: "shell"}, "")
	if len(s.Messages) != 3 {
		t.Fatalf("expected new assistant message after time window, got %d messages", len(s.Messages))
	}
	if len(s.Messages[2].ToolCalls) != 1 || s.Messages[2].ToolCalls[0].ID != "c2" {
		t.Fatalf("new message should contain only c2, got %+v", s.Messages[2])
	}
}

func TestAppendToolCall_DoesNotCoalesceAfterToolResult(t *testing.T) {
	s := New()
	s.AddMessage("user", "goal")
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "c1", Name: "shell"}, "")
	s.AddToolResult("c1", "shell", "output")
	// Next tool call should be new assistant message, not coalesced with previous
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "c2", Name: "shell"}, "")
	if len(s.Messages) != 4 {
		t.Fatalf("expected 4 messages (user, assistant c1, tool result, assistant c2), got %d", len(s.Messages))
	}
	if s.Messages[3].ToolCalls[0].ID != "c2" {
		t.Fatalf("last message should be c2")
	}
}

func TestAppendToolCall_DeduplicatesByID(t *testing.T) {
	s := New()
	s.AddMessage("user", "goal")
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "dup", Name: "shell"}, "")
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "dup", Name: "shell"}, "")
	if len(s.Messages[1].ToolCalls) != 1 {
		t.Fatalf("duplicate ID should not be appended twice, got %d", len(s.Messages[1].ToolCalls))
	}
}
