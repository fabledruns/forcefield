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

// hangingProvider never emits and never closes its stream, ignoring ctx:
// the worst case a transport could present. The run loop must still
// return promptly on cancellation rather than wedging forever.
type hangingProvider struct{}

func (hangingProvider) StreamChat(context.Context, []providers.Message, []tools.Definition) (<-chan providers.StreamEvent, error) {
	return make(chan providers.StreamEvent), nil
}

// partialThenHangingProvider emits one text chunk, then hangs with the
// channel open: cancellation mid-turn must still terminate the run.
type partialThenHangingProvider struct{}

func (partialThenHangingProvider) StreamChat(_ context.Context, _ []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	ch := make(chan providers.StreamEvent, 1)
	ch <- providers.StreamEvent{Text: "partial answer"}
	return ch, nil
}

func TestRun_HungProviderTerminatesOnCancel(t *testing.T) {
	rt := newTestRuntime(hangingProvider{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := rt.RunContext(ctx, []providers.Message{{Role: providers.UserRole, Content: "hi"}})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RunContext() error = nil, want cancellation")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunContext did not return within 5s of cancel; provider stream wedged the loop")
	}
}

func TestRun_PartialStreamTerminatesOnCancel(t *testing.T) {
	rt := newTestRuntime(partialThenHangingProvider{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := rt.RunContext(ctx, []providers.Message{{Role: providers.UserRole, Content: "hi"}})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RunContext() error = nil, want cancellation")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunContext did not return within 5s of cancel after partial output")
	}
}

func TestStreamChat_ReportsCancellationExplicitly(t *testing.T) {
	rt := newTestRuntime(hangingProvider{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := rt.StreamChat(ctx, []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	// The first thinking event proves the model turn is active before the
	// user asks for cancellation.
	select {
	case event := <-events:
		if event.Type != EventThinking {
			t.Fatalf("first event = %v, want EventThinking", event.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
	cancel()

	var cancelled, providerError bool
	for event := range events {
		switch event.Type {
		case EventCancelled:
			cancelled = errors.Is(event.Err, context.Canceled)
		case EventError:
			providerError = true
		}
	}
	if !cancelled {
		t.Fatal("cancellation did not produce EventCancelled with context.Canceled")
	}
	if providerError {
		t.Fatal("cancellation was reported as EventError")
	}
}

type serializedProvider struct {
	active  int32
	max     int32
	calls   int32
	started chan struct{}
}

func (p *serializedProvider) StreamChat(ctx context.Context, _ []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	call := atomic.AddInt32(&p.calls, 1)
	active := atomic.AddInt32(&p.active, 1)
	for {
		max := atomic.LoadInt32(&p.max)
		if active <= max || atomic.CompareAndSwapInt32(&p.max, max, active) {
			break
		}
	}
	if call == 1 {
		close(p.started)
		// Block inside the provider call until cancellation, then
		// unwind synchronously: the runtime holds runMu across the
		// provider call and only releases it once the run returns, so
		// decrementing before return keeps the overlap counter inside
		// the region the runtime actually serializes. Counting a
		// detached background goroutine instead would race the
		// replacement run's first request against orphan teardown
		// that nothing joins, making max==1 scheduling-dependent.
		<-ctx.Done()
		atomic.AddInt32(&p.active, -1)
		ch := make(chan providers.StreamEvent)
		close(ch)
		return ch, nil
	}
	ch := make(chan providers.StreamEvent, 1)
	ch <- providers.StreamEvent{Text: "second run", Done: true}
	close(ch)
	return ch, nil
}

func TestStreamChat_SerializesReplacementRunUntilCancellationCleanup(t *testing.T) {
	p := &serializedProvider{started: make(chan struct{})}
	rt := newTestRuntime(p)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first, err := rt.StreamChat(firstCtx, []providers.Message{{Role: providers.UserRole, Content: "first"}})
	if err != nil {
		t.Fatalf("first StreamChat: %v", err)
	}
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first provider request did not start")
	}

	cancelFirst()
	second, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "second"}})
	if err != nil {
		t.Fatalf("second StreamChat: %v", err)
	}
	for range first {
	}
	var done bool
	for event := range second {
		if event.Type == EventDone {
			done = true
		}
	}
	if !done {
		t.Fatal("replacement run did not complete")
	}
	if got := atomic.LoadInt32(&p.max); got != 1 {
		t.Errorf("concurrent provider requests = %d, want 1", got)
	}
}
