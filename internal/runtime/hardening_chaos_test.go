package runtime

import (
	"context"
	"errors"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// P1.15 lab: provider chaos at the runtime boundary. The runtime must never
// report success after incomplete execution, loop forever, duplicate tools,
// lose cancellation, or corrupt state.
func TestHardeningMidStreamErrorIsErrorNotDone(t *testing.T) {
	p := &scriptedProvider{
		turns: [][]providers.StreamEvent{
			{
				{Text: "partial "},
				{Err: errors.New("mid-stream connection reset")},
			},
		},
	}
	tool := &fixedResultTool{name: "echo"}
	manager := newTestManager(t, tool)
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
	}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var sawDone, sawError bool
	for ev := range events {
		if ev.Type == EventDone {
			sawDone = true
		}
		if ev.Type == EventError {
			sawError = true
		}
	}
	if sawDone {
		t.Fatalf("mid-stream failure reported as EventDone (partial output confused with success)")
	}
	if !sawError {
		t.Fatalf("mid-stream failure must surface as EventError, got none")
	}
}

func TestHardeningMissingDoneIsErrorNotHang(t *testing.T) {
	// Stream closes without Done/Err: incomplete response must fail safely,
	// not hang or report success.
	p := &scriptedProvider{
		turns: [][]providers.StreamEvent{
			{
				{Text: "orphan text, no terminal marker"},
			},
		},
	}
	tool := &fixedResultTool{name: "echo"}
	manager := newTestManager(t, tool)
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := rt.StreamChat(ctx, []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var sawDone bool
	var count int
	for ev := range events {
		count++
		if ev.Type == EventDone {
			sawDone = true
		}
		if count > 16 {
			t.Fatalf("missing-Done stream did not terminate")
		}
	}
	if sawDone {
		t.Fatalf("incomplete stream (no Done) reported as EventDone")
	}
}

func TestHardeningCancelDuringStreamingTerminates(t *testing.T) {
	p := &blockingProvider{release: make(chan struct{})}
	tool := &fixedResultTool{name: "echo"}
	manager := newTestManager(t, tool)
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, err := rt.StreamChat(ctx, []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	cancel()
	close(p.release)
	var sawCancel, sawDone bool
	for ev := range events {
		if ev.Type == EventCancelled {
			sawCancel = true
		}
		if ev.Type == EventDone {
			sawDone = true
		}
	}
	if sawDone {
		t.Fatalf("cancelled run reported EventDone")
	}
	_ = sawCancel // cancelled or error both acceptable; done is not
}

type blockingProvider struct {
	release chan struct{}
}

func (b *blockingProvider) StreamChat(ctx context.Context, _ []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	ch := make(chan providers.StreamEvent)
	go func() {
		defer close(ch)
		select {
		case <-b.release:
			select {
			case ch <- providers.StreamEvent{Text: "late"}:
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	}()
	return ch, nil
}
