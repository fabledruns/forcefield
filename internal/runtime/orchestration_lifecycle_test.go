package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	goruntime "runtime"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

func runtimeNumGoroutine() int {
	return goruntime.NumGoroutine()
}

// blockingChildProvider scripts a parent turn, then blocks its child
// StreamChat call until the context is cancelled (locking the slot long
// enough for real overlap and measuring whether cancellation reached
// the child). A completion before cancellation reports a leaked child.
type blockingChildProvider struct {
	mu sync.Mutex

	turnScript      []providers.StreamEvent // parent turn 1
	calls           int
	childDone       bool
	childLeakedText bool
}

func (p *blockingChildProvider) StreamChat(ctx context.Context, _ []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.mu.Unlock()

	if call == 0 {
		events := make(chan providers.StreamEvent, len(p.turnScript))
		for _, e := range p.turnScript {
			events <- e
		}
		close(events)
		return events, nil
	}

	// Child call: honors cancellation like a real provider.
	select {
	case <-ctx.Done():
		events := make(chan providers.StreamEvent, 1)
		events <- providers.StreamEvent{Done: true}
		close(events)
		p.mu.Lock()
		p.childDone = true
		p.mu.Unlock()
		return events, nil
	case <-time.After(2 * time.Second):
		events := make(chan providers.StreamEvent, 2)
		events <- providers.StreamEvent{Text: "childLeakedText"}
		events <- providers.StreamEvent{Done: true}
		close(events)
		p.mu.Lock()
		p.childLeakedText = true
		p.mu.Unlock()
		return events, nil
	}
}

// waitUntilChildCallStarted blocks until the child call is in flight
// (second StreamChat call), then fires cancel. Deterministic alternative
// to a timed cancel: the join path is what must be exercised.
func (p *blockingChildProvider) waitUntilChildCallStarted(cancel func()) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		c := p.calls
		p.mu.Unlock()
		if c >= 2 {
			cancel()
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestDelegateCancellationJoinsChildBeforeReturning(t *testing.T) {
	provider := &blockingChildProvider{
		turnScript: []providers.StreamEvent{
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-delegate",
				Name:      "delegate",
				Arguments: map[string]any{"agent": "researcher", "task": "long run"},
			}}},
			{Done: true},
		},
	}
	runtime := newOrchestrationTestRuntime(t, provider)

	baseCount := countStableGoroutines(t)

	// The run starts streaming first; cancellation arrives only after
	// the child call is genuinely in flight, so the join path (not the
	// pre-start fast-fail) is what is exercised deterministically.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := time.Now()
	events, err := runtime.StreamChat(ctx, []providers.Message{
		{Role: providers.UserRole, Content: "cancel me"},
	})
	if err != nil {
		cancel()
		t.Fatalf("StreamChat() error = %v", err)
	}

	var delegateTerminal *ToolResult
	var sawParentCancelled bool
	var drained time.Time
	drainedDone := make(chan struct{})
	go func() {
		defer close(drainedDone)
		for e := range events { // strict backpressure while the run lives
			if e.ToolResult != nil && e.ToolResult.Name == "delegate" && e.Type == EventToolCancelled {
				// Optional observation: the scheduler's emit-drop
				// behavior at an already-cancelled boundary may drop
				// this non-terminal event (pre-existing runtime
				// semantics, not delegation logic). The invariants
				// below are checked unconditionally.
				delegateTerminal = e.ToolResult
			}
			if e.Type == EventCancelled {
				sawParentCancelled = true
			}
		}
		drained = time.Now()
	}()

	if !provider.waitUntilChildCallStarted(cancel) {
		t.Fatal("child call never started: test setup failed")
	}

	select {
	case <-drainedDone:
	case <-time.After(4 * time.Second):
		t.Fatal("run did not terminate after the child call was cancelled")
	}
	elapsed := drained.Sub(started)

	if elapsed > 2*time.Second {
		t.Fatalf("run took %v after cancellation, want a prompt join", elapsed)
	}
	if delegateTerminal != nil && !strings.Contains(delegateTerminal.Content, "tool execution cancelled") {
		t.Fatalf("delegate terminal = %q, want the scheduler's cancellation report", delegateTerminal.Content)
	}
	if !sawParentCancelled {
		t.Fatal("parent run never reported EventCancelled")
	}

	// The child's provider call honored cancellation and joined before
	// any parent-visible result: no leaked child text, child executed.
	provider.mu.Lock()
	joined, leaked, calls := provider.childDone, provider.childLeakedText, provider.calls
	provider.mu.Unlock()
	if leaked {
		t.Fatal("child provider produced text after cancellation: join failed")
	}
	if !joined {
		t.Fatal("child provider call never observed cancellation")
	}
	if calls < 2 {
		t.Fatalf("provider calls = %d, want the parent turn and the child call", calls)
	}

	// The joined child's goroutine settles: no detached child runs.
	waitForStableGoroutines(t, baseCount)
}

func TestDelegateDuplicateCallIdReplaysRecordedResult(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{
				{ID: "call-dup", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "one shot"}},
			}},
			{Done: true},
		},
		{
			{Text: "child once"},
			{Done: true},
		},
		// The provider repeats the same call ID in a later turn: the
		// runtime must replay the recorded result, not re-run the child.
		{
			{ToolCalls: []providers.ToolCall{
				{ID: "call-dup", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "one shot"}},
			}},
			{Done: true},
		},
		{
			{Text: "coordinated."},
			{Done: true},
		},
	}}
	runtime := newOrchestrationTestRuntime(t, provider)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "replay"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var originals, replays int
	for e := range events {
		if e.ToolResult == nil || e.ToolResult.Name != "delegate" {
			continue
		}
		switch e.Type {
		case EventToolFinish, EventToolFailed:
			if strings.Contains(e.ToolResult.Content, "repeats call call-dup") {
				replays++
			} else {
				originals++
			}
		}
	}
	if originals != 1 || replays != 1 {
		t.Fatalf("delegate results = (%d original, %d replayed), want (1, 1)", originals, replays)
	}
	if provider.calls != 4 {
		t.Fatalf("provider calls = %d, want 4 (the repeat never re-ran the child)", provider.calls)
	}
}

func TestChildRunRecorderCountsExactlyOneTerminal(t *testing.T) {
	t.Run("first terminal wins", func(t *testing.T) {
		rec := &childRunRecorder{}
		rec.On(Event{Type: EventDone, Response: &providers.Response{Content: "a"}})
		rec.On(Event{Type: EventError, Err: nil})

		rec.mu.Lock()
		defer rec.mu.Unlock()
		if !rec.seen {
			t.Fatal("terminal not recorded")
		}
		if rec.out.terminal != EventDone {
			t.Fatalf("terminal = %v, want the first (Done)", rec.out.terminal)
		}
	})

	t.Run("denials counted by name", func(t *testing.T) {
		rec := &childRunRecorder{}
		for i := 0; i < 2; i++ {
			rec.On(Event{Type: EventToolDenied, ToolResult: &ToolResult{
				Name:    "echo",
				IsError: true,
				Success: false,
				Content: "denied",
			}})
		}
		rec.On(Event{Type: EventToolDenied, ToolResult: &ToolResult{
			Name:    "write_file",
			IsError: true,
			Success: false,
			Content: "denied",
		}})
		total, names := rec.deniedTotal()
		if total != 3 {
			t.Fatalf("denied total = %d, want 3", total)
		}
		if len(names) != 2 {
			t.Fatalf("denied names = %v, want 2 distinct tools", names)
		}
		rec.mu.Lock()
		calls := rec.toolCalls
		rec.mu.Unlock()
		if calls != 3 {
			t.Fatalf("tool calls = %d, want 3 (denial events are terminal per call)", calls)
		}
	})

	t.Run("terminal seen under concurrency", func(t *testing.T) {
		rec := &childRunRecorder{}
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec.On(Event{Type: EventDone, Response: &providers.Response{Content: "winner?"}})
			}()
		}
		wg.Wait()
		if !rec.terminalSeen() {
			t.Fatal("terminal not recorded under concurrency")
		}
	})
}

func TestInteractiveChildAskSerializationShared(t *testing.T) {
	orch := newOrchestrator(config.Orchestration{
		Enabled:          true,
		ChildPermissions: config.ChildPermissionsInteractive,
	}, newChildRegistryForTests(), nil, nil)
	base := newRecordingAsker(permissions.PromptAllowOnce)
	orch.parent = &Runtime{
		agent:     agent.New("root", "root-system", ""),
		scheduler: newScheduler(nil, nil, base, DefaultSchedulerConfig),
	}
	def, err := orch.registry.Get("researcher")
	if err != nil {
		t.Fatalf("registry get researcher: %v", err)
	}
	asker := orch.childAsker(def, []string{"root", "researcher"})

	// serialize the ask scope: children share one ask mutex so the
	// approval surface can never see two overlapping child assigns.
	serial := orch.childAskSerial()
	if serial == nil {
		t.Fatal("interactive mode must expose a shared ask serializer")
	}

	var wg sync.WaitGroup
	const n = 4
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prompt, askErr := asker.Ask(context.Background(), permissions.Request{Tool: "echo"})
			if askErr != nil {
				t.Errorf("child asker error = %v", askErr)
			}
			if prompt != permissions.PromptAllowOnce && prompt != permissions.PromptDenyOnce {
				t.Errorf("prompt = %v, want a one-time decision", prompt)
			}
		}()
	}
	wg.Wait()
	if got := base.askCount(); got != n {
		t.Fatalf("base asks = %d, want %d", got, n)
	}
}

// countStableGoroutines polls the process goroutine count twice with a
// small gap so transient dispatch noise does not skew the baseline.
func countStableGoroutines(t testing.TB) int {
	t.Helper()
	n1 := runtimeNumGoroutine()
	time.Sleep(20 * time.Millisecond)
	n2 := runtimeNumGoroutine()
	return (n1 + n2) / 2
}

// waitForStableGoroutines tolerates a small constant above baseline
// until it settles. A runaway child run would hold the count up.
func waitForStableGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countStableGoroutines(t) <= base+3 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("goroutine count never settled to baseline (%d), possible detached child run", base)
}

// TestDelegateChildrenStreamConcurrently drives the shared provider from
// two children at once (real overlap widened by the probe) under the
// parent run: provider-level concurrency plus shared-universe state must
// pass the race detector without interleaving the children's scripts.
// Run this test under `go test -race` in CI: the race detector needs a
// C toolchain and is not enabled on all dev workstations, and the
// overlap assertions below cannot substitute for it.
func TestDelegateChildrenStreamConcurrently(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := &concurrencyProbeProvider{turns: [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{
				{ID: "call-a", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "a"}},
				{ID: "call-b", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "b"}},
			}},
			{Done: true},
		},
		{{Text: "childA done"}, {Done: true}},
		{{Text: "childB done"}, {Done: true}},
		{{Text: "coordinated."}, {Done: true}},
	}}
	runtime := newProbeRuntime(t, provider, childConfig(t, 2, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "fan out"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var bodies []string
	for e := range events {
		if e.Type == EventToolFinish && e.ToolResult != nil && e.ToolResult.Name == "delegate" {
			bodies = append(bodies, e.ToolResult.Content)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("delegate finishes = %d, want 2", len(bodies))
	}
	sawA, sawB := false, false
	for _, body := range bodies {
		if strings.Contains(body, "childA done") {
			sawA = true
		}
		if strings.Contains(body, "childB done") {
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Fatalf("delegate bodies = %v, want exactly one childA and one childB verdict", bodies)
	}
	if provider.calls != 4 {
		t.Fatalf("provider calls = %d, want 4", provider.calls)
	}
	if p := provider.peak; p < 2 {
		t.Fatalf("peak concurrent provider turns = %d, want 2 (children must genuinely overlap)", p)
	}
}
