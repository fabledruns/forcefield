package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// slowTool blocks on a channel until released, letting tests observe that
// independent calls overlap in time.
type slowTool struct {
	name    string
	release <-chan struct{}
	started chan<- string
}

func (t *slowTool) Name() string                     { return t.name }
func (t *slowTool) Description() string               { return "slow" }
func (t *slowTool) InputSchema() map[string]any        { return map[string]any{"type": "object"} }
func (t *slowTool) Execute(ctx context.Context, _ map[string]any) (tools.Result, error) {
	if t.started != nil {
		t.started <- t.name
	}
	select {
	case <-t.release:
	case <-ctx.Done():
		return tools.Result{}, ctx.Err()
	}
	return tools.Result{Content: t.name + "-done"}, nil
}

func newTestManager(t *testing.T, ts ...tools.Tool) *tools.Manager {
	t.Helper()
	m := tools.NewManager(tools.NewRegistry())
	for _, tool := range ts {
		if err := m.Register(tool); err != nil {
			t.Fatalf("register tool: %v", err)
		}
	}
	return m
}

func TestScheduler_RunsIndependentCallsConcurrently(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 2)

	manager := newTestManager(t,
		&slowTool{name: "a", release: release, started: started},
		&slowTool{name: "b", release: release, started: started},
	)
	s := newScheduler(manager, SchedulerConfig{MaxConcurrency: 2, MaxRetries: 0, BaseBackoff: time.Millisecond})

	calls := []providers.ToolCall{
		{ID: "1", Name: "a"},
		{ID: "2", Name: "b"},
	}

	done := make(chan []ToolResult, 1)
	go func() {
		done <- s.Run(context.Background(), calls, func(Event) bool { return true })
	}()

	// Both tools should start before either is released, proving they ran
	// concurrently rather than sequentially.
	seen := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-timeout:
			t.Fatalf("timed out waiting for both tools to start concurrently, saw %v", seen)
		}
	}

	close(release)
	results := <-done

	if len(results) != 2 || results[0].Content != "a-done" || results[1].Content != "b-done" {
		t.Fatalf("results = %#v, want ordered a-done/b-done", results)
	}
}

func TestScheduler_PreservesResultOrderRegardlessOfCompletionOrder(t *testing.T) {
	fast := &fixedResultTool{name: "fast", delay: 0}
	slow := &fixedResultTool{name: "slow", delay: 30 * time.Millisecond}

	manager := newTestManager(t, slow, fast)
	s := newScheduler(manager, SchedulerConfig{MaxConcurrency: 4, MaxRetries: 0, BaseBackoff: time.Millisecond})

	calls := []providers.ToolCall{
		{ID: "1", Name: "slow"},
		{ID: "2", Name: "fast"},
	}

	results := s.Run(context.Background(), calls, func(Event) bool { return true })
	if results[0].Name != "slow" || results[1].Name != "fast" {
		t.Fatalf("results out of order: %#v", results)
	}
}

type fixedResultTool struct {
	name  string
	delay time.Duration
}

func (t *fixedResultTool) Name() string              { return t.name }
func (t *fixedResultTool) Description() string        { return "fixed" }
func (t *fixedResultTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *fixedResultTool) Execute(ctx context.Context, _ map[string]any) (tools.Result, error) {
	if t.delay > 0 {
		select {
		case <-time.After(t.delay):
		case <-ctx.Done():
			return tools.Result{}, ctx.Err()
		}
	}
	return tools.Result{Content: t.name}, nil
}

// flakyTool fails its first N attempts with an execution error, then
// succeeds. Used to exercise retry behavior.
type flakyTool struct {
	failuresLeft int32
	meta         tools.Metadata
}

func (t *flakyTool) Name() string              { return "flaky" }
func (t *flakyTool) Description() string        { return "flaky" }
func (t *flakyTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *flakyTool) Metadata() tools.Metadata    { return t.meta }
func (t *flakyTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	if atomic.AddInt32(&t.failuresLeft, -1) >= 0 {
		return tools.Result{}, errors.New("transient failure")
	}
	return tools.Result{Content: "ok"}, nil
}

func TestScheduler_RetriesRetryableFailures(t *testing.T) {
	flaky := &flakyTool{failuresLeft: 1, meta: tools.Metadata{Retryable: true}}
	manager := newTestManager(t, flaky)
	s := newScheduler(manager, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 2, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "flaky"}}, func(Event) bool { return true })
	if len(results) != 1 || results[0].IsError || results[0].Content != "ok" {
		t.Fatalf("results = %#v, want a successful retried result", results)
	}
	if results[0].Attempt != 2 {
		t.Errorf("Attempt = %d, want 2 (one failure then a success)", results[0].Attempt)
	}
}

func TestScheduler_DoesNotRetryNonRetryableFailures(t *testing.T) {
	flaky := &flakyTool{failuresLeft: 100, meta: tools.Metadata{Retryable: false}}
	manager := newTestManager(t, flaky)
	s := newScheduler(manager, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 3, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "flaky"}}, func(Event) bool { return true })
	if len(results) != 1 || !results[0].IsError || results[0].Attempt != 1 {
		t.Fatalf("results = %#v, want a single failed attempt with no retries", results)
	}
}

func TestScheduler_OneFailureDoesNotCancelSiblings(t *testing.T) {
	failing := &fixedErrorTool{name: "failing"}
	ok := &fixedResultTool{name: "ok"}

	manager := newTestManager(t, failing, ok)
	s := newScheduler(manager, SchedulerConfig{MaxConcurrency: 2, MaxRetries: 0, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{
		{ID: "1", Name: "failing"},
		{ID: "2", Name: "ok"},
	}, func(Event) bool { return true })

	if !results[0].IsError {
		t.Errorf("results[0] should have failed")
	}
	if results[1].IsError || results[1].Content != "ok" {
		t.Errorf("results[1] should have succeeded unaffected by its sibling, got %#v", results[1])
	}
}

type fixedErrorTool struct{ name string }

func (t *fixedErrorTool) Name() string              { return t.name }
func (t *fixedErrorTool) Description() string        { return "fails" }
func (t *fixedErrorTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *fixedErrorTool) Execute(context.Context, map[string]any) (tools.Result, error) {
	return tools.Result{IsError: true, Content: "boom"}, nil
}

func TestScheduler_TimeoutProducesFailedResult(t *testing.T) {
	blocking := &blockingTool{meta: tools.Metadata{Timeout: 10 * time.Millisecond}}
	manager := newTestManager(t, blocking)
	s := newScheduler(manager, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	start := time.Now()
	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "blocking"}}, func(Event) bool { return true })
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("scheduler took %s, expected timeout to cut it short", elapsed)
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %#v, want a timed-out (failed) result", results)
	}
}

type blockingTool struct{ meta tools.Metadata }

func (t *blockingTool) Name() string              { return "blocking" }
func (t *blockingTool) Description() string        { return "blocks forever" }
func (t *blockingTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *blockingTool) Metadata() tools.Metadata    { return t.meta }
func (t *blockingTool) Execute(ctx context.Context, _ map[string]any) (tools.Result, error) {
	<-ctx.Done()
	return tools.Result{}, ctx.Err()
}

func TestScheduler_StreamingToolEmitsProgress(t *testing.T) {
	st := &streamingTool{}
	manager := newTestManager(t, st)
	s := newScheduler(manager, DefaultSchedulerConfig)

	var mu sync.Mutex
	var chunks []string
	emit := func(e Event) bool {
		if e.Type == EventToolProgress {
			mu.Lock()
			chunks = append(chunks, e.ToolProgress.Data)
			mu.Unlock()
		}
		return true
	}

	s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "streaming"}}, emit)

	mu.Lock()
	defer mu.Unlock()
	if len(chunks) != 2 || chunks[0] != "line1" || chunks[1] != "line2" {
		t.Fatalf("chunks = %v, want [line1 line2]", chunks)
	}
}

type streamingTool struct{}

func (streamingTool) Name() string              { return "streaming" }
func (streamingTool) Description() string        { return "streams" }
func (streamingTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (streamingTool) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	return streamingTool{}.ExecuteStream(ctx, args, nil)
}
func (streamingTool) ExecuteStream(_ context.Context, _ map[string]any, onChunk func(tools.StreamChunk)) (tools.Result, error) {
	if onChunk != nil {
		onChunk(tools.StreamChunk{Stream: "stdout", Data: "line1"})
		onChunk(tools.StreamChunk{Stream: "stdout", Data: "line2"})
	}
	return tools.Result{Content: "line1\nline2\n"}, nil
}
