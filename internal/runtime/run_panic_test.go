package runtime

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// panicTool blows up instead of returning, modelling a buggy tool
// implementation.
type panicTool struct{ name string }

func (t *panicTool) Name() string                { return t.name }
func (t *panicTool) Description() string         { return "panics" }
func (t *panicTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *panicTool) Execute(context.Context, map[string]any) (tools.Result, error) {
	panic("boom")
}

// panicProvider blows up inside the model-turn call, modelling a buggy
// provider adapter. The panic happens on the run goroutine itself, so it
// exercises the run-level boundary rather than the scheduler-worker one.
type panicProvider struct{}

func (panicProvider) StreamChat(context.Context, []providers.Message, []tools.Definition) (<-chan providers.StreamEvent, error) {
	panic("provider boom")
}

// TestScheduler_PanickingToolFailsCallNotProcess pins that a tool panic
// becomes a failed tool result (paired with its ToolStart), not a dead
// test process — and that the scheduler stays usable afterwards, proving
// worker cleanup (WaitGroup, semaphore) still completed.
func TestScheduler_PanickingToolFailsCallNotProcess(t *testing.T) {
	manager := newTestManager(t, &panicTool{name: "boom"}, &fixedResultTool{name: "ok"})
	s := newScheduler(manager, nil, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: 0})

	var events []Event
	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "boom"}}, func(e Event) bool {
		events = append(events, e)
		return true
	})
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if results[0].Success || !results[0].IsError {
		t.Fatalf("result = %+v, want a failed error result", results[0])
	}
	if !strings.Contains(results[0].Content, "panicked") || !strings.Contains(results[0].Content, "boom") {
		t.Errorf("content = %q, want it to name the panic", results[0].Content)
	}
	if results[0].Err == nil {
		t.Error("result Err is nil, want the panic recorded")
	}

	var starts, terminals int
	for _, e := range events {
		switch e.Type {
		case EventToolStart:
			starts++
		case EventToolFinish, EventToolFailed, EventToolCancelled, EventToolDenied:
			terminals++
		}
	}
	if starts != 1 || terminals != 1 {
		t.Errorf("starts = %d terminals = %d, want exactly 1 of each (pairing intact)", starts, terminals)
	}

	// Reuse through the same single slot: if the panicking worker leaked
	// its semaphore or WaitGroup count, this hangs until the test times
	// out instead of succeeding.
	again := s.Run(context.Background(), []providers.ToolCall{{ID: "2", Name: "ok"}}, func(Event) bool { return true })
	if len(again) != 1 || !again[0].Success {
		t.Fatalf("reuse results = %+v, want success (scheduler wedged by panic)", again)
	}
}

// TestRun_ProviderPanicBecomesError pins that a panic on the run
// goroutine surfaces as an observable run error — not a crashed process
// — and that the runtime stays usable, proving run-level cleanup
// (runMu release) still completed.
func TestRun_ProviderPanicBecomesError(t *testing.T) {
	rt := newTestRuntime(panicProvider{})
	msgs := []providers.Message{{Role: providers.UserRole, Content: "hi"}}

	_, err := rt.RunContext(context.Background(), msgs)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("RunContext() error = %v, want a panic-surfaced error", err)
	}

	// A wedged runMu would block this second run forever; success proves
	// the deferred cleanup ran before the error was reported.
	_, err = rt.RunContext(context.Background(), msgs)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("second RunContext() error = %v, want a repeatable panic error (run wedged)", err)
	}
}
