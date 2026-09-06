package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// attemptProvider scripts one StreamChat outcome per model-turn attempt:
// either a connection-phase error or a sequence of stream events. Calls
// beyond the script fail the test loudly instead of hanging.
type attemptOutcome struct {
	callErr error
	events  []providers.StreamEvent
}

type attemptProvider struct {
	outcomes []attemptOutcome
	calls    atomic.Int32
}

func (p *attemptProvider) StreamChat(_ context.Context, _ []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	n := int(p.calls.Add(1)) - 1
	if n >= len(p.outcomes) {
		panic("provider called more times than scripted")
	}
	out := p.outcomes[n]
	if out.callErr != nil {
		return nil, out.callErr
	}
	ch := make(chan providers.StreamEvent, len(out.events))
	for _, e := range out.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func textTurn(text string) []providers.StreamEvent {
	return []providers.StreamEvent{{Text: text}, {Done: true, StopReason: providers.FinishStop}}
}

func TestTurnRetry_CleanTimeoutThenSuccess(t *testing.T) {
	p := &attemptProvider{outcomes: []attemptOutcome{
		{callErr: context.DeadlineExceeded},
		{events: textTurn("recovered")},
	}}
	rt := newTestRuntime(p)

	resp, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("RunContext() error = %v, want recovery on second attempt", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("content = %q, want recovered", resp.Content)
	}
	if got := p.calls.Load(); got != 2 {
		t.Errorf("provider calls = %d, want exactly 2", got)
	}
}

func TestTurnRetry_PersistentTimeoutFailsBoundedAndTransient(t *testing.T) {
	p := &attemptProvider{outcomes: []attemptOutcome{
		{callErr: context.DeadlineExceeded},
		{callErr: context.DeadlineExceeded},
		{callErr: context.DeadlineExceeded},
		{callErr: context.DeadlineExceeded}, // must never run: 1 + 2 retries
	}}
	rt := newTestRuntime(p)

	_, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err == nil {
		t.Fatal("RunContext() error = nil, want failure after bounded retries")
	}
	if got := p.calls.Load(); got != 3 {
		t.Errorf("provider calls = %d, want 3 (initial + 2 retries)", got)
	}
	if !IsTransientError(err) {
		t.Errorf("error = %v, want it marked transient", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want the Timeout cause preserved for errors.Is", err)
	}
}

func TestTurnRetry_NonRetryableFailsFast(t *testing.T) {
	p := &attemptProvider{outcomes: []attemptOutcome{
		{callErr: errors.New("boom")},
		{events: textTurn("must not run")},
	}}
	rt := newTestRuntime(p)

	_, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err == nil {
		t.Fatal("expected the plain error to surface")
	}
	if got := p.calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (no retry for non-transient)", got)
	}
	if IsTransientError(err) {
		t.Errorf("plain error must not be marked transient: %v", err)
	}
}

func TestTurnRetry_CancelledNeverRetried(t *testing.T) {
	p := &attemptProvider{outcomes: []attemptOutcome{
		{callErr: context.Canceled},
		{events: textTurn("must not run")},
	}}
	rt := newTestRuntime(p)

	_, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if got := p.calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}
	if IsTransientError(err) {
		t.Errorf("cancellation must not be marked transient: %v", err)
	}
}

func TestTurnRetry_PartialTextNeverReplayed(t *testing.T) {
	p := &attemptProvider{outcomes: []attemptOutcome{
		{events: []providers.StreamEvent{
			{Text: "partial"},
			{Err: context.DeadlineExceeded},
		}},
		{events: textTurn("must not run")},
	}}
	rt := newTestRuntime(p)

	_, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err == nil {
		t.Fatal("expected the mid-stream failure to surface")
	}
	if got := p.calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (partial output must not be replayed)", got)
	}
	// The failure itself is transient (a fresh turn may succeed) even
	// though this turn correctly refused to retry automatically.
	if !IsTransientError(err) {
		t.Errorf("mid-stream timeout should still be marked transient: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want the Timeout cause preserved", err)
	}
}

func TestTurnRetry_EmittedToolCallsNeverRetried(t *testing.T) {
	counter := &countingTool{}
	p := &attemptProvider{outcomes: []attemptOutcome{
		{events: []providers.StreamEvent{
			{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "count", Arguments: map[string]any{}}}},
			{Err: context.DeadlineExceeded},
		}},
		{events: textTurn("must not run")},
	}}
	rt := newTestRuntimeWithLimits(p, DefaultLimits, counter)

	_, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err == nil {
		t.Fatal("expected the mid-stream failure to surface")
	}
	if got := p.calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (emitted tool calls must never be re-requested)", got)
	}
	if got := counter.calls.Load(); got != 0 {
		t.Errorf("tool executed %d times, want 0 (scheduler runs only after a successful turn)", got)
	}
	if !IsTransientError(err) {
		t.Errorf("error should be marked transient for a manual retry: %v", err)
	}
}

func TestTurnRetry_CancelDuringBackoffReturnsPromptly(t *testing.T) {
	p := &attemptProvider{outcomes: []attemptOutcome{
		{callErr: context.DeadlineExceeded},
		{events: textTurn("must not run")},
	}}
	rt := newTestRuntime(p)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := rt.RunContext(ctx, []providers.Message{{Role: providers.UserRole, Content: "hi"}})
		done <- err
	}()

	// The first attempt fails fast; cancel while the retry backoff sleeps.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled from the interrupted backoff", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunContext did not return within 5s of cancel during retry backoff")
	}
	if got := p.calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (no retry after cancel)", got)
	}
}

func TestTurnRetry_RetryDoesNotDuplicateToolExecution(t *testing.T) {
	counter := &countingTool{}
	p := &attemptProvider{outcomes: []attemptOutcome{
		{callErr: context.DeadlineExceeded},
		{events: toolCallTurn("c1", "count")},
		{events: textTurn("finished")},
	}}
	rt := newTestRuntimeWithLimits(p, DefaultLimits, counter)

	resp, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	if resp.Content != "finished" {
		t.Errorf("content = %q, want finished", resp.Content)
	}
	if got := p.calls.Load(); got != 3 {
		t.Errorf("provider calls = %d, want 3 (fail, tool turn, final)", got)
	}
	if got := counter.calls.Load(); got != 1 {
		t.Errorf("tool executed %d times, want exactly 1", got)
	}
}
