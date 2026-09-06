package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
)

// TestScheduler_CancelDuringAskEmitsToolCancelled pins that cancelling
// while a permission prompt is open reports ToolCancelled — not a denial
// — so the transcript, session, and failure accounting see the truth.
func TestScheduler_CancelDuringAskEmitsToolCancelled(t *testing.T) {
	ran := false
	tool := &fnTool{name: "shell", fn: func() { ran = true }}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	unblock := make(chan struct{})
	asker := permissions.AskerFunc(func(ctx context.Context, _ permissions.Request) (permissions.Prompt, error) {
		select {
		case <-ctx.Done():
			return permissions.PromptDenyOnce, ctx.Err()
		case <-unblock:
			return permissions.PromptAllowOnce, nil
		}
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	var events []Event
	var mu sync.Mutex
	done := make(chan []ToolResult, 1)
	go func() {
		done <- s.Run(ctx, []providers.ToolCall{{ID: "1", Name: "shell"}}, func(e Event) bool {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
			return true
		})
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case results := <-done:
		if len(results) != 1 {
			t.Fatalf("results len = %d, want 1", len(results))
		}
		if results[0].Success {
			t.Error("cancelled ask should not succeed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not return after cancellation")
	}
	if ran {
		t.Error("tool executed despite cancellation during approval")
	}

	mu.Lock()
	defer mu.Unlock()
	var sawStart, sawCancelled, sawDenied bool
	for _, e := range events {
		switch e.Type {
		case EventToolStart:
			sawStart = true
		case EventToolCancelled:
			sawCancelled = true
		case EventToolDenied:
			sawDenied = true
		}
	}
	if !sawStart {
		t.Error("missing EventToolStart")
	}
	if !sawCancelled {
		t.Error("cancel during Ask must emit EventToolCancelled")
	}
	if sawDenied {
		t.Error("cancel during Ask must not emit EventToolDenied")
	}
}

// TestScheduler_CancelBeforeStartReturnsCancelledCleanly pins that calls
// which never reached a worker report cancellation without emitting a
// ToolStart the TUI would have to pair with a terminal event.
func TestScheduler_CancelBeforeStartReturnsCancelledCleanly(t *testing.T) {
	tool := &fixedResultTool{name: "echo"}
	manager := newTestManager(t, tool)
	s := newScheduler(manager, nil, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var starts int
	results := s.Run(ctx, []providers.ToolCall{
		{ID: "1", Name: "echo"},
		{ID: "2", Name: "echo"},
	}, func(e Event) bool {
		if e.Type == EventToolStart {
			starts++
		}
		return true
	})

	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	for i, r := range results {
		if r.Success || !r.IsError {
			t.Errorf("results[%d] = %+v, want a cancelled error result", i, r)
		}
	}
	if starts != 0 {
		t.Errorf("emitted %d ToolStart events for never-started calls, want 0", starts)
	}
}

// TestScheduler_ConcurrentCancelExitsWithoutLeak cancels a batch of
// in-flight tools and requires every worker to exit promptly with a
// terminal state: no hangs, and every ToolStart pairs with exactly one
// terminal event even under -race.
func TestScheduler_ConcurrentCancelExitsWithoutLeak(t *testing.T) {
	release := make(chan struct{})
	manager := newTestManager(t,
		&slowTool{name: "a", release: release},
		&slowTool{name: "b", release: release},
		&slowTool{name: "c", release: release},
		&slowTool{name: "d", release: release},
	)
	s := newScheduler(manager, nil, nil, SchedulerConfig{MaxConcurrency: 4, MaxRetries: 2, BaseBackoff: time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	calls := []providers.ToolCall{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b"},
		{ID: "3", Name: "c"},
		{ID: "4", Name: "d"},
	}
	var events []Event
	var mu sync.Mutex
	done := make(chan []ToolResult, 1)
	go func() {
		done <- s.Run(ctx, calls, func(e Event) bool {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
			return true
		})
	}()

	time.Sleep(200 * time.Millisecond) // let all four workers start
	cancel()

	select {
	case results := <-done:
		if len(results) != len(calls) {
			t.Fatalf("results len = %d, want %d", len(results), len(calls))
		}
		for i, r := range results {
			if r.Success {
				t.Errorf("results[%d] succeeded despite cancellation: %+v", i, r)
			}
		}
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("scheduler workers did not exit after cancellation (leak/hang)")
	}

	mu.Lock()
	defer mu.Unlock()
	terminal := map[string]int{}
	starts := map[string]int{}
	for _, e := range events {
		switch e.Type {
		case EventToolStart:
			starts[e.ToolCall.ID]++
		case EventToolFinish, EventToolFailed, EventToolCancelled, EventToolDenied:
			terminal[e.ToolResult.ToolCallID]++
		}
	}
	for _, c := range calls {
		if starts[c.ID] != 1 {
			t.Errorf("call %s has %d ToolStart events, want exactly 1", c.ID, starts[c.ID])
		}
		if terminal[c.ID] != 1 {
			t.Errorf("call %s has %d terminal events, want exactly 1", c.ID, terminal[c.ID])
		}
	}
}
