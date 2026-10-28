package runtime

// Parent-side rewrite of the wave3l-e1 child deliverable (kept its six
// test names and assertions). The delivered draft never produced a
// file: its five tool calls all failed and the runtime honestly stopped
// it as blocked ("stopped after 5 consecutive tool failures"). Rewritten
// here against the real wiring pattern of orchestration_test.go.

import (
	"context"
	"strings"
	"testing"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// hardeningSink records orchestration events so tests can assert the
// orchestration-event contract without a filesystem writer.
type hardeningSink struct {
	events []orchevents.Event
}

func (s *hardeningSink) Emit(e orchevents.Event) {
	s.events = append(s.events, e)
}

func (s *hardeningSink) ofType(t orchevents.Type) []orchevents.Event {
	var out []orchevents.Event
	for _, e := range s.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// newHardeningRuntime wires the same universe as
// newOrchestrationTestRuntime, but with the orchestration bounds under
// test and a recording sink attached directly to the orchestrator (the
// white-box equivalent of Runtime.SetOrchestrationEventSink).
func newHardeningRuntime(t *testing.T, p providers.ModelProvider, cfg config.Orchestration) (*Runtime, *hardeningSink) {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	if err := full.Register(echoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	registry := newChildRegistryForTests()
	orch := newOrchestrator(cfg, registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
	}
	sink := &hardeningSink{}
	orch.setSink(sink)
	r := &Runtime{
		provider:    p,
		agent:       agent.New("root", "root-system", ""),
		activeAgent: "root",
		manager:     full,
		fullManager: full,
		orch:        orch,
		scheduler:   newScheduler(full, nil, nil, DefaultSchedulerConfig),
	}
	orch.parent = r
	return r, sink
}

// delegateResult finds the parent-visible delegate tool result in a
// drained event list (terminal events carry it).
func delegateResult(all []Event) *ToolResult {
	for _, e := range all {
		if e.ToolResult == nil || e.ToolResult.Name != "delegate" {
			continue
		}
		if e.Type == EventToolFinish || e.Type == EventToolFailed || e.Type == EventToolCancelled || e.Type == EventToolDenied {
			return e.ToolResult
		}
	}
	return nil
}

func TestHardeningTaskCharsRejectionEmitsEvent(t *testing.T) {
	long := strings.Repeat("x", 64)
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{ToolCalls: []providers.ToolCall{{
			ID:        "call-1",
			Name:      "delegate",
			Arguments: map[string]any{"agent": "researcher", "task": long},
		}}}, {Done: true}},
		{{Text: "handled locally"}, {Done: true}},
	}}
	rt, sink := newHardeningRuntime(t, provider, config.Orchestration{
		Enabled:      true,
		MaxTaskChars: intPtr(16),
	})

	events, err := rt.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "go"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	all, done, _ := collect(events)
	if done == nil {
		t.Fatal("parent run never completed after the rejection")
	}

	rejections := sink.ofType(orchevents.DelegationRejected)
	if len(rejections) != 1 {
		t.Fatalf("DelegationRejected events = %d, want exactly 1", len(rejections))
	}
	got := rejections[0]
	if got.Budget != orchevents.BudgetTaskChars {
		t.Errorf("Budget = %q, want %q", got.Budget, orchevents.BudgetTaskChars)
	}
	if got.BudgetLimit != 16 || got.BudgetValue != 64 {
		t.Errorf("BudgetLimit/BudgetValue = %d/%d, want 16/64", got.BudgetLimit, got.BudgetValue)
	}
	if strings.TrimSpace(got.Reason) == "" {
		t.Error("rejection carried no reason text")
	}

	result := delegateResult(all)
	if result == nil {
		t.Fatal("no parent-visible delegate result")
	}
	if !strings.Contains(result.Content, "exceeding the limit") {
		t.Errorf("delegate result = %q, want the task-chars message", result.Content)
	}
}

func TestHardeningUnknownAgentRejectionEmitsEvent(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{ToolCalls: []providers.ToolCall{{
			ID:        "call-1",
			Name:      "delegate",
			Arguments: map[string]any{"agent": "ghost", "task": "do work"},
		}}}, {Done: true}},
		{{Text: "handled locally"}, {Done: true}},
	}}
	rt, sink := newHardeningRuntime(t, provider, config.Orchestration{Enabled: true})

	events, err := rt.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "go"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	all, _, _ := collect(events)

	rejections := sink.ofType(orchevents.DelegationRejected)
	if len(rejections) != 1 {
		t.Fatalf("DelegationRejected events = %d, want exactly 1", len(rejections))
	}
	if !strings.Contains(rejections[0].Reason, "unknown agent") {
		t.Errorf("reason = %q, want the unknown-agent message", rejections[0].Reason)
	}
	result := delegateResult(all)
	if result == nil || !strings.Contains(result.Content, "unknown agent") {
		t.Errorf("delegate result = %v, want the unknown-agent message", result)
	}
}

func TestHardeningSetSinkNilSafe(t *testing.T) {
	provider := &scriptedProvider{turns: orchestrationTurns("do echo")}
	rt, _ := newHardeningRuntime(t, provider, config.Orchestration{Enabled: true})
	rt.SetOrchestrationEventSink(nil) // must not panic, and the run still works

	events, err := rt.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "go"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	_, done, _ := collect(events)
	if done == nil {
		t.Fatal("run with a nil sink never completed")
	}
}

func TestHardeningChildDeniedCounterRaisesDenied(t *testing.T) {
	rec := &childRunRecorder{agentName: "coding", budget: maxChildProgressLines, childRunID: "c1"}
	rec.On(Event{Type: EventToolDenied, ToolResult: &ToolResult{ToolCallID: "t1", Name: "write_file"}})
	rec.On(Event{Type: EventToolDenied, ToolResult: &ToolResult{ToolCallID: "t2", Name: "write_file"}})
	rec.On(Event{Type: EventDone})

	total, names := rec.deniedTotal()
	if total != 2 {
		t.Errorf("denied total = %d, want 2", total)
	}
	if len(names) != 1 || names[0] != "write_file" {
		t.Errorf("denied tools = %v, want [write_file]", names)
	}

	result := composeDelegateResult("coding", rec)
	if !strings.Contains(result.Content, "denied=2") {
		t.Errorf("delegate header = %q, want denied=2", result.Content)
	}
}

func TestHardeningParentCancelStopsChildHonestly(t *testing.T) {
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
	rt := newOrchestrationTestRuntime(t, provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := rt.StreamChat(ctx, []providers.Message{
		{Role: providers.UserRole, Content: "cancel me"},
	})
	if err != nil {
		cancel()
		t.Fatalf("StreamChat() error = %v", err)
	}

	var delegateCancelled int
	var sawParentCancelled bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range events {
			if e.Type == EventToolCancelled && e.ToolResult != nil && e.ToolResult.Name == "delegate" {
				delegateCancelled++
			}
			if e.Type == EventCancelled {
				sawParentCancelled = true
			}
		}
	}()

	if !provider.waitUntilChildCallStarted(cancel) {
		t.Fatal("child call never started: test setup failed")
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("run did not terminate after the child call was cancelled")
	}

	if delegateCancelled > 1 {
		t.Errorf("delegate cancelled terminals = %d, want at most one (first terminal wins)", delegateCancelled)
	}
	if !sawParentCancelled {
		t.Error("parent run never reported EventCancelled")
	}
	provider.mu.Lock()
	joined, leaked := provider.childDone, provider.childLeakedText
	provider.mu.Unlock()
	if leaked {
		t.Error("child provider produced text after cancellation: join failed")
	}
	if !joined {
		t.Error("child provider call never observed cancellation")
	}
}

func TestHardeningStopBoundaryBlockedNotSuccess(t *testing.T) {
	echoTurn := []providers.StreamEvent{
		{ToolCalls: []providers.ToolCall{{ID: "echo-loop", Name: "echo", Arguments: map[string]any{"value": "tick"}}}},
		{Done: true},
	}
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		// Parent turn 1: delegate to the looping child.
		{{ToolCalls: []providers.ToolCall{{
			ID:        "call-1",
			Name:      "delegate",
			Arguments: map[string]any{"agent": "researcher", "task": "loop forever"},
		}}}, {Done: true}},
		// Child turns: echo, echo, echo, echo. The iteration cap must
		// stop the child as blocked at turn 5 - before it consumes the
		// parent's closing turn.
		echoTurn, echoTurn, echoTurn, echoTurn,
		// Parent closes with its own answer after the child stopped.
		{{Text: "child stopped, wrapping up"}, {Done: true}},
	}}
	rt, _ := newHardeningRuntime(t, provider, config.Orchestration{Enabled: true})
	// Children clone the parent snapshot (runChild: childSnap :=
	// *scope.snap), so the runtime-wide limit IS the child's limit. The
	// child burns its four echo turns and blocks; the parent needs only
	// two iterations of its own and still closes honestly.
	rt.limits = Limits{MaxIterations: 4, MaxToolCalls: 100, MaxConsecutiveFailures: 5}

	events, err := rt.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "go"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	all, done, _ := collect(events)
	if done == nil {
		t.Fatal("parent run never completed after the child stopped")
	}

	result := delegateResult(all)
	if result == nil {
		t.Fatal("no parent-visible delegate result")
	}
	if !result.IsError {
		t.Errorf("delegate result is not an error; the stop boundary must not read as success: %+v", result)
	}
	if !strings.Contains(result.Content, "blocked") {
		t.Errorf("delegate result = %q, want the blocked stop boundary", result.Content)
	}
	if strings.Contains(result.Content, "child status=verified") {
		t.Error("blocked child reported a verified status: dishonest terminal state")
	}
}
