package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/providers"
	"forcefield/internal/task"
	"forcefield/internal/tools"
)

// failingTool always returns a deterministic (non-error, tool-reported)
// failure, so the scheduler never retries it and the runtime's
// consecutive-failure counter climbs by exactly one per call.
type failingTool struct{}

func (failingTool) Name() string        { return "failing" }
func (failingTool) Description() string { return "always fails" }
func (failingTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (failingTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	return tools.Result{IsError: true, Content: "boom"}, nil
}

// countingTool records how many times it actually ran, so tests can assert
// a limit stopped the runtime *before* execution rather than after.
type countingTool struct {
	calls atomic.Int32
}

func (t *countingTool) Name() string        { return "count" }
func (t *countingTool) Description() string { return "counts calls" }
func (t *countingTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *countingTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	t.calls.Add(1)
	return tools.Result{Content: "ok"}, nil
}

func toolCallTurn(id, name string) []providers.StreamEvent {
	return []providers.StreamEvent{
		{ToolCalls: []providers.ToolCall{{ID: id, Name: name, Arguments: map[string]any{}}}},
		{Done: true},
	}
}

func newTestRuntimeWithLimits(p providers.ModelProvider, limits Limits, extra ...tools.Tool) *Runtime {
	manager := tools.NewManager(tools.NewRegistry())
	for _, tool := range extra {
		if err := manager.Register(tool); err != nil {
			panic(err)
		}
	}
	if err := manager.Register(newUpdateTaskStateTool()); err != nil {
		panic(err)
	}

	return &Runtime{
		provider:  p,
		agent:     agent.New("test", "system", ""),
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		limits:    limits,
	}
}

// collect drains a StreamChat event channel and returns the events plus
// convenience pointers to the final EventDone/EventBlocked payloads.
func collect(events <-chan Event) (all []Event, done, blocked *Event) {
	for e := range events {
		e := e
		all = append(all, e)
		switch e.Type {
		case EventDone:
			done = &e
		case EventBlocked:
			blocked = &e
		}
	}
	return all, done, blocked
}

func TestRun_ConsecutiveFailuresStopAsBlocked(t *testing.T) {
	turns := make([][]providers.StreamEvent, 10)
	for i := range turns {
		turns[i] = toolCallTurn("call", "failing")
	}
	provider := &scriptedProvider{turns: turns}
	rt := newTestRuntimeWithLimits(provider, Limits{MaxConsecutiveFailures: 3}, failingTool{})

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "do it"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	_, done, blocked := collect(events)
	if done != nil {
		t.Fatalf("got EventDone, want the run to stop as blocked instead: %#v", done)
	}
	if blocked == nil {
		t.Fatalf("expected EventBlocked after repeated tool failures, got none")
	}
	if blocked.Status != task.StatusBlocked {
		t.Errorf("blocked.Status = %v, want %v", blocked.Status, task.StatusBlocked)
	}
	if blocked.TaskState == nil || blocked.TaskState.ConsecutiveFailures < 3 {
		t.Errorf("TaskState.ConsecutiveFailures = %+v, want >= 3", blocked.TaskState)
	}
	// Exactly 3 model turns should have run: the loop must stop as soon as
	// the failure streak hits the limit, not keep going.
	if provider.calls != 3 {
		t.Errorf("provider.calls = %d, want 3 (loop should stop right at the limit)", provider.calls)
	}
}

func TestRun_MaxIterationsStopsAsBlocked(t *testing.T) {
	turns := make([][]providers.StreamEvent, 10)
	for i := range turns {
		turns[i] = toolCallTurn("call", "count")
	}
	provider := &scriptedProvider{turns: turns}
	counter := &countingTool{}
	rt := newTestRuntimeWithLimits(provider, Limits{MaxIterations: 2}, counter)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "do it"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	_, done, blocked := collect(events)
	if done != nil {
		t.Fatalf("got EventDone, want blocked: %#v", done)
	}
	if blocked == nil {
		t.Fatalf("expected EventBlocked after hitting max iterations")
	}
	if provider.calls != 2 {
		t.Errorf("provider.calls = %d, want 2 (MaxIterations)", provider.calls)
	}
}

func TestRun_MaxToolCallsStopsBeforeExecutingOverLimit(t *testing.T) {
	turn := []providers.StreamEvent{
		{ToolCalls: []providers.ToolCall{
			{ID: "1", Name: "count", Arguments: map[string]any{}},
			{ID: "2", Name: "count", Arguments: map[string]any{}},
			{ID: "3", Name: "count", Arguments: map[string]any{}},
		}},
		{Done: true},
	}
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{turn}}
	counter := &countingTool{}
	rt := newTestRuntimeWithLimits(provider, Limits{MaxToolCalls: 2}, counter)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "do it"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	_, done, blocked := collect(events)
	if done != nil {
		t.Fatalf("got EventDone, want blocked: %#v", done)
	}
	if blocked == nil {
		t.Fatalf("expected EventBlocked when a single turn would exceed MaxToolCalls")
	}
	if got := counter.calls.Load(); got != 0 {
		t.Errorf("tool executed %d times, want 0 (limit must be enforced before execution)", got)
	}
}

func TestRun_VerifiedStatusRequiresExplicitVerification(t *testing.T) {
	turns := [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-1",
				Name: "update_task_state",
				Arguments: map[string]any{
					"verification":      "passed",
					"verification_note": "go test ./... passed",
				},
			}}},
			{Done: true},
		},
		{
			{Text: "Done, tests pass."},
			{Done: true},
		},
	}
	provider := &scriptedProvider{turns: turns}
	rt := newTestRuntimeWithLimits(provider, Limits{})

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "fix it and verify"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	_, done, blocked := collect(events)
	if blocked != nil {
		t.Fatalf("unexpected EventBlocked: %#v", blocked)
	}
	if done == nil {
		t.Fatalf("expected EventDone")
	}
	if done.Status != task.StatusVerified {
		t.Errorf("Status = %v, want %v", done.Status, task.StatusVerified)
	}
	if done.TaskState == nil || done.TaskState.Verification != task.VerificationPassed {
		t.Errorf("TaskState.Verification = %+v, want %v", done.TaskState, task.VerificationPassed)
	}
}

func TestRun_ToolUseWithoutVerificationIsPartial(t *testing.T) {
	provider := &scriptedProvider{turns: testTurns()}
	rt := newTestRuntime(provider)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "use a tool"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	_, done, _ := collect(events)
	if done == nil {
		t.Fatalf("expected EventDone")
	}
	if done.Status != task.StatusPartial {
		t.Errorf("Status = %v, want %v (tool used, never verified)", done.Status, task.StatusPartial)
	}
}

func TestRun_NoToolsIsVerifiedByDefault(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{Text: "The capital of France is Paris."}, {Done: true}},
	}}
	rt := newTestRuntime(provider)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "capital of France?"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	_, done, _ := collect(events)
	if done == nil {
		t.Fatalf("expected EventDone")
	}
	if done.Status != task.StatusVerified {
		t.Errorf("Status = %v, want %v (plain answer, no tools involved)", done.Status, task.StatusVerified)
	}
}

func TestRun_RecoversFromToolFailureAndContinues(t *testing.T) {
	turns := [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{ID: "1", Name: "failing", Arguments: map[string]any{}}}},
			{Done: true},
		},
		{
			// The model sees the failure and tries a different tool.
			{ToolCalls: []providers.ToolCall{{ID: "2", Name: "count", Arguments: map[string]any{}}}},
			{Done: true},
		},
		{
			{Text: "Recovered and finished."},
			{Done: true},
		},
	}
	provider := &scriptedProvider{turns: turns}
	counter := &countingTool{}
	rt := newTestRuntimeWithLimits(provider, Limits{MaxConsecutiveFailures: 5}, failingTool{}, counter)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "do it"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	_, done, blocked := collect(events)
	if blocked != nil {
		t.Fatalf("unexpected EventBlocked: %#v", blocked)
	}
	if done == nil || done.Response.Content != "Recovered and finished." {
		t.Fatalf("done = %#v, want a final response after recovering from the failure", done)
	}
	if got := counter.calls.Load(); got != 1 {
		t.Errorf("counting tool ran %d times, want 1", got)
	}
}

func TestRun_ContextCancellationStopsTheLoop(t *testing.T) {
	provider := &scriptedProvider{turns: testTurns()}
	rt := newTestRuntime(provider)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rt.RunContext(ctx, []providers.Message{{Role: providers.UserRole, Content: "use a tool"}})
	if err == nil {
		t.Fatalf("RunContext() error = nil, want a cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunContext() error = %v, want context.Canceled", err)
	}
}
