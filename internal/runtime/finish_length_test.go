package runtime

import (
	"context"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

func TestFinishLengthIsBlockedNotDone(t *testing.T) {
	// Provider returns a single turn with FinishLength and no tool calls, simulating
	// output truncation.
	p := &scriptedProvider{
		turns: [][]providers.StreamEvent{
			{
				{Text: "partial answer..."},
				{Done: true, StopReason: providers.FinishLength},
			},
		},
	}
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   tools.NewManager(tools.NewRegistry()),
		scheduler: newScheduler(tools.NewManager(tools.NewRegistry()), nil, nil, DefaultSchedulerConfig),
	}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	var sawBlocked, sawDone bool
	for ev := range events {
		if ev.Type == EventBlocked {
			sawBlocked = true
		}
		if ev.Type == EventDone {
			sawDone = true
		}
	}
	if !sawBlocked {
		t.Fatal("expected EventBlocked for FinishLength, got none")
	}
	if sawDone {
		t.Fatal("FinishLength should not emit EventDone")
	}
}

func TestFinishLengthWithToolCallsIsNotBlocked(t *testing.T) {
	// If model returns tool calls together with length, we still execute tools
	// (length refers to content, not tool calls). This test ensures we don't
	// block when tool calls are present.
	tool := &fixedResultTool{name: "echo"}
	manager := newTestManager(t, tool)
	p := &scriptedProvider{
		turns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "echo", Arguments: map[string]any{"value": "x"}}}, StopReason: providers.FinishLength, Done: true},
			},
			{
				{Text: "done", Done: true},
			},
		},
	}
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
	}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	var sawDone, sawBlocked bool
	for ev := range events {
		if ev.Type == EventDone {
			sawDone = true
		}
		if ev.Type == EventBlocked {
			sawBlocked = true
		}
	}
	// With tool calls, FinishLength should still proceed to tool execution and not immediately block.
	// The second turn ends with Done, so final should be Done, not Blocked due to first.
	if sawBlocked {
		t.Fatal("FinishLength with tool calls should not immediately block")
	}
	if !sawDone {
		t.Fatal("expected eventual EventDone after tool execution")
	}
}
