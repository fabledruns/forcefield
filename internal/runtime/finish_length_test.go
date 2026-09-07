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

func TestFinishLengthWithToolCallsIsBlocked(t *testing.T) {
	// P1.17 hardening (RC6): length-truncated tool calls must Block without
	// executing. Partial argument JSON is not an operation the model
	// completed; running it would confuse incomplete output with success.
	// This replaces the RC5 behavior pinned here previously.
	counter := &countingTool{}
	manager := newTestManager(t, counter)
	p := &scriptedProvider{
		turns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "count", Arguments: map[string]any{}}}, StopReason: providers.FinishLength, Done: true},
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
	var sawDone, sawBlocked, sawTool bool
	for ev := range events {
		if ev.Type == EventDone {
			sawDone = true
		}
		if ev.Type == EventBlocked {
			sawBlocked = true
		}
		if ev.Type == EventToolFinish || ev.Type == EventToolFailed {
			sawTool = true
		}
	}
	if !sawBlocked {
		t.Fatal("FinishLength with tool calls must emit EventBlocked")
	}
	if sawDone {
		t.Fatal("FinishLength with tool calls must not emit EventDone")
	}
	if sawTool || counter.calls.Load() != 0 {
		t.Fatal("FinishLength with tool calls must not execute tools")
	}
}
