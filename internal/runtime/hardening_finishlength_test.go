package runtime

import (
	"context"
	"testing"

	"forcefield/internal/providers"
)

// P1.15 reproduction for RC5 C03: FinishLength WITH tool calls currently
// executes truncated arguments. The runtime must never confuse partial
// model output with successful completion: length-truncated tool calls
// must Block without executing.
//
// This test FAILS on RC5 baseline (tool executes) and PASSES after P1.17.
func TestHardeningFinishLengthWithToolCallsBlocksWithoutExecuting(t *testing.T) {
	execCounting := &countingTool{}
	manager2 := newTestManager(t, execCounting)
	p := &scriptedProvider{
		turns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "count", Arguments: map[string]any{}}}, StopReason: providers.FinishLength, Done: true},
			},
			{
				{Text: "done", Done: true},
			},
		},
	}
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager2,
		scheduler: newScheduler(manager2, nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
	}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var sawBlocked, sawDone, sawToolFinish bool
	for ev := range events {
		switch ev.Type {
		case EventBlocked:
			sawBlocked = true
		case EventDone:
			sawDone = true
		case EventToolFinish, EventToolFailed:
			sawToolFinish = true
		}
	}
	if !sawBlocked {
		t.Fatalf("FinishLength+tool_calls must emit EventBlocked (partial output is not success)")
	}
	if sawDone {
		t.Fatalf("FinishLength+tool_calls must not emit EventDone")
	}
	if sawToolFinish {
		t.Fatalf("FinishLength+tool_calls must not execute tools (truncated args)")
	}
	if execCounting.calls.Load() != 0 {
		t.Fatalf("tool executed %d times on truncated turn, want 0", execCounting.calls.Load())
	}
}
