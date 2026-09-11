package runtime

import (
	"context"
	"testing"

	"forcefield/internal/providers"
)

// TestDuplicateToolCallIDKeepsSingleHistoryPair drives two model turns
// that echo the same tool-call ID: execution must reuse the recorded
// result (exactly once) and the provider-visible history must contain
// only one assistant/result pair with that ID.
func TestDuplicateToolCallIDKeepsSingleHistoryPair(t *testing.T) {
	counter := &countingTool{}
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		toolCallTurn("dup-1", "count"),
		{
			{ToolCalls: []providers.ToolCall{{ID: "dup-1", Name: "count", Arguments: map[string]any{}}}},
			{Done: true},
		},
		textTurn("finished"),
	}}
	rt := newTestRuntimeWithLimits(provider, DefaultLimits, counter)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "go"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	for range events {
	}
	if got := counter.calls.Load(); got != 1 {
		t.Fatalf("tool executed %d times, want exactly 1 (idempotency preserved)", got)
	}
	if len(provider.messages) != 3 {
		t.Fatalf("provider turns = %d, want 3", len(provider.messages))
	}
	// The third turn's input is the full in-memory history: it must
	// carry exactly one assistant pair and one result for dup-1.
	third := provider.messages[2]
	assistants, results := 0, 0
	for _, m := range third {
		for _, tc := range m.ToolCalls {
			if tc.ID == "dup-1" {
				assistants++
			}
		}
		if m.Role == providers.ToolRole && m.ToolCallID == "dup-1" {
			results++
		}
	}
	if assistants != 1 || results != 1 {
		t.Errorf("history has %d assistant + %d results for dup-1, want 1+1", assistants, results)
	}
}

// TestUniqueToolCallIDsUnchanged ensures the dedup filter leaves normal
// distinct IDs alone: two different calls still produce two pairs.
func TestUniqueToolCallIDsUnchanged(t *testing.T) {
	counter := &countingTool{}
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		toolCallTurn("u-1", "count"),
		toolCallTurn("u-2", "count"),
		textTurn("finished"),
	}}
	rt := newTestRuntimeWithLimits(provider, DefaultLimits, counter)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "go"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	for range events {
	}
	if got := counter.calls.Load(); got != 2 {
		t.Fatalf("tool executed %d times, want 2 (unique calls run)", got)
	}
	if len(provider.messages) != 3 {
		t.Fatalf("provider turns = %d, want 3", len(provider.messages))
	}
	third := provider.messages[2]
	assistants := 0
	for _, m := range third {
		assistants += len(m.ToolCalls)
	}
	if assistants != 2 {
		t.Errorf("assistant tool calls in history = %d, want 2", assistants)
	}
}
