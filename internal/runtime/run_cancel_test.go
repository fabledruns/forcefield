package runtime

import (
	"context"
	"errors"
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
