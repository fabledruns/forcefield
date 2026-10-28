package runtime

// Parent-side rewrite of the wave3l-e2 child deliverable (kept its five
// test names and assertions). The delivered draft redefined
// orchestrationShared and childRunRecorder inside the test file, mocked
// the sink, and asserted mock semantics that contradict the product
// ("turns remain 1" past the limit, when consumeTurn keeps counting).
// Rewritten here against the real recorder, the real shared pool, and
// the real emit path.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// budgetRaceSink records orchestration events for assertions.
type budgetRaceSink struct {
	mu     sync.Mutex
	events []orchevents.Event
}

func (s *budgetRaceSink) Emit(e orchevents.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *budgetRaceSink) ofType(t orchevents.Type) []orchevents.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []orchevents.Event
	for _, e := range s.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// newBudgetRaceRuntime wires the orchestration universe with the
// aggregate child-turn limit under test and a recording sink.
func newBudgetRaceRuntime(t *testing.T, p providers.ModelProvider, maxTotalTurns int) (*Runtime, *budgetRaceSink) {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	if err := full.Register(echoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	registry := newChildRegistryForTests()
	orch := newOrchestrator(config.Orchestration{
		Enabled:            true,
		MaxTotalChildTurns: intPtr(maxTotalTurns),
	}, registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
	}
	sink := &budgetRaceSink{}
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

func TestBudgetRaceMarkerChargesDeltaDoesNot(t *testing.T) {
	shared := &orchestrationShared{}
	rec := &childRunRecorder{
		budget:     maxChildProgressLines,
		agentName:  "worker",
		shared:     shared,
		turnLimit:  3,
		childRunID: "c1",
	}
	for i := 0; i < 3; i++ {
		rec.On(Event{Type: EventThinking}) // inference-start markers charge
	}
	rec.On(Event{Type: EventThinking, Thinking: "reasoning delta"}) // deltas never charge

	if rec.turns != 3 {
		t.Errorf("recorder turns = %d, want 3 (markers count, deltas do not)", rec.turns)
	}
	if shared.turns != 3 {
		t.Errorf("shared turns = %d, want 3", shared.turns)
	}
}

func TestBudgetRaceFiresExactlyOnce(t *testing.T) {
	echoTurn := func() []providers.StreamEvent {
		return []providers.StreamEvent{
			{ToolCalls: []providers.ToolCall{{ID: "echo-loop", Name: "echo", Arguments: map[string]any{"value": "tick"}}}},
			{Done: true},
		}
	}
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{ToolCalls: []providers.ToolCall{{
			ID:        "call-1",
			Name:      "delegate",
			Arguments: map[string]any{"agent": "researcher", "task": "loop"},
		}}}, {Done: true}},
		echoTurn(), echoTurn(), echoTurn(), echoTurn(),
		{{Text: "wrapped up"}, {Done: true}},
	}}
	rt, sink := newBudgetRaceRuntime(t, provider, 1)

	events, err := rt.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "go"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	collect(events)

	fired := sink.ofType(orchevents.BudgetExhausted)
	if len(fired) != 1 {
		t.Fatalf("BudgetExhausted events = %d, want exactly 1 (first crossing fires once)", len(fired))
	}
	got := fired[0]
	if got.Budget != orchevents.BudgetTotalTurns {
		t.Errorf("Budget = %q, want %q", got.Budget, orchevents.BudgetTotalTurns)
	}
	if got.BudgetLimit != 1 || got.BudgetValue != 2 {
		t.Errorf("BudgetLimit/BudgetValue = %d/%d, want 1/2 (the crossing charge)", got.BudgetLimit, got.BudgetValue)
	}
	if strings.TrimSpace(got.Reason) == "" {
		t.Error("BudgetExhausted carried no reason text")
	}
}

func TestBudgetRaceConcurrentRecordersNoLostCharges(t *testing.T) {
	shared := &orchestrationShared{}
	const recorders = 8
	const markersPerRecorder = 25
	const turnLimit = 1000

	var wg sync.WaitGroup
	errs := make(chan string, recorders)
	for g := 0; g < recorders; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rec := &childRunRecorder{
				budget:     0, // no progress lines: emissions are not under test here
				agentName:  fmt.Sprintf("worker-%d", g),
				shared:     shared,
				turnLimit:  turnLimit,
				childRunID: fmt.Sprintf("c%d", g),
			}
			for i := 0; i < markersPerRecorder; i++ {
				rec.On(Event{Type: EventThinking})
			}
			if rec.turns != markersPerRecorder {
				errs <- fmt.Sprintf("worker-%d turns = %d, want %d", g, rec.turns, markersPerRecorder)
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}

	if shared.turns != recorders*markersPerRecorder {
		t.Errorf("shared turns = %d, want %d (no lost charges)", shared.turns, recorders*markersPerRecorder)
	}
}

func TestBudgetRaceCancelledChildOthersKeepConsuming(t *testing.T) {
	shared := &orchestrationShared{}
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	first := &childRunRecorder{
		budget: 0, agentName: "a", shared: shared, turnLimit: 1,
		budgetCancel: cancel1, childCtx: ctx1, childRunID: "c1",
	}
	second := &childRunRecorder{
		budget: 0, agentName: "b", shared: shared, turnLimit: 1,
		budgetCancel: cancel2, childCtx: ctx2, childRunID: "c2",
	}

	first.On(Event{Type: EventThinking})  // total 1: in budget
	first.On(Event{Type: EventThinking})  // total 2: crosses, first child cancels
	second.On(Event{Type: EventThinking}) // total 3: second child also cancels honestly

	if ctx1.Err() == nil {
		t.Error("first child not cancelled by its own over-limit charge")
	}
	if ctx2.Err() == nil {
		t.Error("second child not cancelled by its own over-limit charge")
	}
	if shared.turns != 3 {
		t.Errorf("shared turns = %d, want 3 (every charge counts)", shared.turns)
	}
	if !first.overBudget || !second.overBudget {
		t.Error("both recorders must remember the budget cancellation for their reports")
	}
}

func TestBudgetRaceBudgetNoteInResult(t *testing.T) {
	rec := &childRunRecorder{
		budget:     maxChildProgressLines,
		agentName:  "coding",
		shared:     &orchestrationShared{},
		turnLimit:  1,
		childRunID: "c1",
		overBudget: true,
	}
	rec.On(Event{Type: EventCancelled}) // the cancelled terminal the budget produced

	result := composeDelegateResult("coding", rec)
	if !result.IsError {
		t.Error("budget-cancelled delegation must report as an error, not success")
	}
	if !strings.Contains(result.Content, "aggregate child-turn budget exhausted") {
		t.Errorf("delegate result = %q, want the aggregate budget note", result.Content)
	}
}
