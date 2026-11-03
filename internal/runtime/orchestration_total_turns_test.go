package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// TestSharedTurnBudgetCountsAndFiresOnce pins the aggregate
//
//	child-turn budget semantics: every turn counts, crossing the limit
//
// is reported exactly once, but every over-limit charge stays
// over-limit for cancellation purposes.
func TestSharedTurnBudgetCountsAndFiresOnce(t *testing.T) {
	sh := &orchestrationShared{}
	const limit = 5

	for i := 1; i <= limit; i++ {
		total, over, fire := sh.consumeTurn(limit)
		if total != i || over || fire {
			t.Fatalf("charge %d: total=%d over=%v fire=%v, expected in-budget", i, total, over, fire)
		}
	}
	total, over, fire := sh.consumeTurn(limit)
	if total != limit+1 || !over || !fire {
		t.Fatalf("first crossing: total=%d over=%v fire=%v", total, over, fire)
	}
	// Every subsequent charge stays over-limit but reports no more.
	total2, over2, fire2 := sh.consumeTurn(limit)
	if total2 != limit+2 || !over2 || fire2 {
		t.Fatalf("second crossing: total=%d over=%v fire=%v", total2, over2, fire2)
	}
}

// TestChildRecorderAggregateBudgetCancelsChild pins the recorder-side
// wiring: the offending child's context is cancelled once the shared
// pool is crossed, the recorder remembers that for the report, and
// other children consuming later keep the same honest accounting.
func TestChildRecorderAggregateBudgetCancelsChild(t *testing.T) {
	shared := &orchestrationShared{}
	cctx, cancel := context.WithCancel(context.Background())
	rec := &childRunRecorder{
		budget:       maxChildProgressLines,
		agentName:    "worker",
		shared:       shared,
		turnLimit:    2,
		budgetCancel: cancel,
		childCtx:     cctx,
		childRunID:   "c1",
	}

	// Two in-budget turns do not cancel. Reasoning deltas streaming
	// inside an inference must not charge: wave-1 counted deltas as
	// turns, which exhausted the pool mid-model-turn.
	rec.On(Event{Type: EventThinking})
	rec.On(Event{Type: EventThinking, Thinking: "first inference deltas"})
	rec.On(Event{Type: EventThinking})
	rec.On(Event{Type: EventThinking, Thinking: "second inference deltas"})
	if cctx.Err() != nil {
		t.Fatal("child cancelled before the limit was crossed; reasoning deltas leaked into the budget")
	}
	if rec.turns != 2 {
		t.Errorf("turns = %d, want 2 (markers count, deltas do not)", rec.turns)
	}
	if rec.overBudget {
		t.Error("budget flag set before the limit was crossed")
	}

	// The third turn crosses the shared pool: the offending child
	// cancels and the flag lands for the report.
	rec.On(Event{Type: EventThinking})
	rec.On(Event{Type: EventThinking, Thinking: "post-budget delta ignored"})
	if cctx.Err() == nil {
		t.Fatal("expected child cancellation once the aggregate budget was crossed")
	}
	if !rec.overBudget {
		t.Error("recorder must remember the budget cancellation for the report")
	}
	if rec.turns != 3 {
		t.Errorf("turns = %d, want 3 (post-budget delta must not count)", rec.turns)
	}
	if shared.turns != 3 {
		t.Errorf("shared turns = %d, want 3", shared.turns)
	}

	// Another child sharing the pool: its over-limit charge also
	// cancels itself (honest), but only the first crossing reported.
	otherCtx, otherCancel := context.WithCancel(context.Background())
	t.Cleanup(otherCancel)
	other := &childRunRecorder{
		shared:       shared,
		turnLimit:    2,
		agentName:    "b",
		budgetCancel: otherCancel,
		childCtx:     otherCtx,
		childRunID:   "c2",
	}
	other.On(Event{Type: EventThinking})
	if otherCtx.Err() == nil {
		t.Fatal("expected the second over-limit child to cancel too")
	}
	if !other.overBudget {
		t.Error("second child must also record its budget cancellation")
	}
}

// TestBudgetConcurrencyNoLostCharges races the shared counter to prove
// the mutex keeps the aggregate exact under parallel schedulers.
func TestBudgetConcurrencyNoLostCharges(t *testing.T) {
	shared := &orchestrationShared{}
	const limit = 10

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				shared.consumeTurn(limit)
			}
		}()
	}
	wg.Wait()

	if got := shared.turns; got != 8*25 {
		t.Errorf("lost charges: turns = %d, want %d", got, 8*25)
	}
}

// fanTransient fabricates one clean pre-token provider failure with the
// classification the transports produce for a 503 or a stall timeout
// (deadline-exceeded kind). No package-private statusError is built: the
// wrapper routes through errors.Is to context.DeadlineExceeded, so
// providers.IsTransient reports true, canRetryTurn accepts the retry
// (nothing was emitted), and providers.TurnRetryDelay applies the
// production bounded backoff between the attempts.
type fanTransient struct{}

func (fanTransient) Error() string {
	return "providers: simulated clean pre-token transient failure (timeout kind)"
}

func (e fanTransient) Unwrap() error { return context.DeadlineExceeded }

const fanParentIdentity = "fan-parent-run"

// fanBudgetScripts holds the immutable script shared by the parent
// provider instance and every run-scoped clone: parent turns strictly in
// order, child turns keyed by the delegated task text, and one clean
// pre-token transient failure injected into the first attempt of the
// retrying children.
type fanBudgetScripts struct {
	parentTurns [][]providers.StreamEvent
	childTurns  map[string][][]providers.StreamEvent
	childFail   map[string]error

	// cloneSeq hands out one instance id per clone (0 stays reserved for
	// the original parent instance). Each clone is registered below so
	// the test can snapshot every run-scoped instance's ledger.
	cloneSeq atomic.Int64
	clonesMu sync.Mutex
	clones   map[int64]*fanBudgetProvider
}

func newFanBudgetScripts() *fanBudgetScripts {
	return &fanBudgetScripts{clones: map[int64]*fanBudgetProvider{}}
}

// registerClone remembers a cloned run-scoped instance for the test's
// per-instance assertions.
func (s *fanBudgetScripts) registerClone(p *fanBudgetProvider) {
	s.clonesMu.Lock()
	defer s.clonesMu.Unlock()
	s.clones[p.instanceID] = p
}

// registeredClones returns every clone in id order.
func (s *fanBudgetScripts) registeredClones() []*fanBudgetProvider {
	s.clonesMu.Lock()
	defer s.clonesMu.Unlock()
	out := make([]*fanBudgetProvider, 0, len(s.clones))
	for i := int64(1); i <= s.cloneSeq.Load(); i++ {
		out = append(out, s.clones[i])
	}
	return out
}

// fanBudgetProvider is a scripted test ModelProvider participating in
// run-scoped cloning exactly like the built-in adapters: every delegated
// child run receives its own clone (providers.CloneForRun moves mutable
// state out of the shared path), so the per-instance call ledger proves
// the isolation a shared instance could never show. The immutable
// scripts are the test analog of the adapters' shared configuration.
type fanBudgetProvider struct {
	scripts    *fanBudgetScripts
	instanceID int64 // 0 = original parent instance; >=1 = one per clone

	mu      sync.Mutex
	calls   map[string]int // identity -> attempts served by this instance
	callLog []string       // "id%d %s#%d ctxdead=%v" for diagnosis
	ctxDead int            // calls invoked with an already-cancelled context
}

// CloneForRun hands out one fresh run-scoped instance sharing only the
// immutable scripts; nothing mutable crosses into the child run.
func (p *fanBudgetProvider) CloneForRun() (providers.ModelProvider, error) {
	clone := &fanBudgetProvider{
		scripts:    p.scripts,
		instanceID: p.scripts.cloneSeq.Add(1),
	}
	p.scripts.registerClone(clone)
	return clone, nil
}

func (p *fanBudgetProvider) StreamChat(ctx context.Context, messages []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	identity := fanParentIdentity
	if len(messages) > 0 && strings.Contains(messages[0].Content, "child-system") {
		// Child call: the delegated task text is the last user message.
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == providers.UserRole {
				identity = messages[i].Content
				break
			}
		}
	}

	dead := ctx.Err() != nil

	p.mu.Lock()
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	n := p.calls[identity] + 1
	p.calls[identity] = n
	if dead {
		p.ctxDead++
	}
	p.callLog = append(p.callLog, fmt.Sprintf("id%d %s#%d ctxdead=%v", p.instanceID, identity, n, dead))
	p.mu.Unlock()

	// A call on a dead context never consults the script: the built-in
	// adapters observe the context and refuse the request, so the child
	// records an actual cancellation instead of an invented outcome.
	if dead {
		return nil, ctx.Err()
	}

	var turn []providers.StreamEvent
	if identity != fanParentIdentity {
		if err := p.scripts.childFail[identity]; err != nil && n == 1 {
			return nil, fmt.Errorf("provider 503 while %q (simulated pre-token failure): %w", identity, err)
		}
		turns, ok := p.scripts.childTurns[identity]
		if !ok || n > len(turns) {
			return nil, context.Canceled // out of script for this child
		}
		turn = turns[n-1]
	} else {
		if n > len(p.scripts.parentTurns) {
			return nil, context.Canceled // out of parent script
		}
		turn = p.scripts.parentTurns[n-1]
	}

	out := make(chan providers.StreamEvent, len(turn))
	for _, event := range turn {
		out <- event
	}
	close(out)
	return out, nil
}

// snapshot copies the per-instance ledger under its mutex.
func (p *fanBudgetProvider) snapshot() (calls map[string]int, ctxDead int, log []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	calls = make(map[string]int, len(p.calls))
	for identity, n := range p.calls {
		calls[identity] = n
	}
	return calls, p.ctxDead, append([]string(nil), p.callLog...)
}

// TestOrchestrationConcurrentRetryAtTurnBudgetBoundary races retry
// accounting, run-scoped provider cloning, and the aggregate child-turn
// budget in one real orchestration run: the parent fans out four
// concurrent sibling children (DefaultSchedulerConfig.MaxConcurrency =
// 4) on cloned providers, the shared pool has max_total_child_turns =
// 4, and two of the four children fail their first attempt with a clean
// pre-token transient error.
//
// Charging is structurally ordered, with no harness timing involved:
//
//   - Every child's first model turn starts at spawn, so the four first
//     attempts are the four in-budget charges (<=4 stays under the
//     limit, so no cancellation can ever preempt them: both transient
//     errors are always delivered; the spawn order between them is a
//     deliberate race the assertions are order-free about).
//   - The two healthy children are one-turn runs and finish in
//     microseconds. A retry attempt's marker cannot be emitted before
//     the production retry backoff (providers.TurnRetryDelay: 1s base,
//     half jitter => >=500ms), so the 5th and 6th charges — the ones
//     that cross max_total_child_turns — are always the retry
//     attempts of the two erroring children. Shared.counter crossing
//     fires BudgetExhausted exactly once (dedup'd by consumeTurn) and
//     cancels the offending child on every over-limit charge.
//
// Asserted invariants hold under all interleavings: aggregate charges
// exactly 6 (no lost or invented charges), per-child charge counts
// (healthy exactly one, retrying exactly its two attempts), exactly one
// budget_exhausted event with the crossing charge on it, two healthy and
// two budget-exhausted delegate results, and run-scoped provider
// isolation: each clone serves exactly its own child, the 1-per-child
// mutable ledgers never cross, and the boundary retry attempt is invoked
// on a context the budget already cancelled.
func TestOrchestrationConcurrentRetryAtTurnBudgetBoundary(t *testing.T) {
	scripts := newFanBudgetScripts()
	scripts.parentTurns = [][]providers.StreamEvent{
		{
			// One assistant turn, four concurrent sibling delegations.
			{ToolCalls: []providers.ToolCall{
				delegateCall("fan-call-a", "researcher", "fan-a"),
				delegateCall("fan-call-b", "researcher", "fan-b"),
				delegateCall("fan-call-c", "researcher", "fan-c"),
				delegateCall("fan-call-d", "researcher", "fan-d"),
			}},
			{Done: true},
		},
		{{Text: "wrapped up the fan-out"}, {Done: true}},
	}
	scripts.childTurns = map[string][][]providers.StreamEvent{
		"fan-a": oneTextTurn("fan-a artifacts merged"),
		"fan-c": oneTextTurn("fan-c artifacts merged"),
		// The retry turn would only be served if the budget cancellation
		// had failed to happen: a completed retry is visible through both
		// the per-instance ledger and the delegate results.
		"fan-b": oneTextTurn("fan-b merged after retry"),
		"fan-d": oneTextTurn("fan-d merged after retry"),
	}
	scripts.childFail = map[string]error{
		"fan-b": fanTransient{},
		"fan-d": fanTransient{},
	}

	parent := &fanBudgetProvider{scripts: scripts}
	rt, sink := newBudgetRaceRuntime(t, parent, 4) // max_total_child_turns = 4

	events, err := rt.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "fan out"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	all, _, _ := collect(events)

	// Every delegation spawned and joined exactly one child run.
	if got := sink.ofType(orchevents.AgentSpawned); len(got) != 4 {
		t.Fatalf("AgentSpawned events = %d, want 4 concurrent sibling children", len(got))
	}
	if got := sink.ofType(orchevents.ChildCompleted); len(got) != 4 {
		t.Fatalf("ChildCompleted events = %d, want 4 (every child joins unconditionally)", len(got))
	}

	// Exact aggregate charge accounting, read back from the run-level
	// progress events: one ChildProgress per charged attempt marker,
	// cumulative per child. Healthy: one charge each; retrying: both of
	// its attempts charged (a failed attempt counts before its retry).
	progress := sink.ofType(orchevents.ChildProgress)
	if len(progress) != 6 {
		t.Fatalf("child progress markers = %d, want 6 exact aggregate charges (1+2+1+2)", len(progress))
	}
	perChild := map[string][]int{} // ChildRunID -> consecutive charge values
	for _, e := range progress {
		perChild[e.ChildRunID] = append(perChild[e.ChildRunID], e.Turns)
	}
	if len(perChild) != 4 {
		t.Fatalf("four distinct child runs must carry the progress events: %v", perChild)
	}
	oneAttempt, twoAttempts := 0, 0
	for runID, turns := range perChild {
		for i, got := range turns {
			if got != i+1 {
				t.Fatalf("child %s attempt %d: progress Turns = %d, want the consecutive charge count %d", runID, i+1, got, i+1)
			}
		}
		switch len(turns) {
		case 1:
			oneAttempt++
		case 2:
			twoAttempts++
		default:
			t.Fatalf("child %s charged %d model turns, want exactly 1 (healthy) or 2 (one retry)", runID, len(turns))
		}
	}
	if oneAttempt != 2 || twoAttempts != 2 {
		t.Fatalf("the two healthy children must charge once each and the two retrying children twice each: one-attempt children = %d, two-attempt children = %d", oneAttempt, twoAttempts)
	}

	// The boundary crossing fired exactly once, on the first over-limit
	// charge — the 5th, which structurally is the first retry attempt.
	fired := sink.ofType(orchevents.BudgetExhausted)
	if len(fired) != 1 {
		t.Fatalf("BudgetExhausted events = %d, want exactly 1 (consumeTurn dedups the fire)", len(fired))
	}
	ev := fired[0]
	if ev.Budget != orchevents.BudgetTotalTurns {
		t.Errorf("Budget = %v, want %v", ev.Budget, orchevents.BudgetTotalTurns)
	}
	if ev.BudgetLimit != 4 || ev.BudgetValue != 5 {
		t.Errorf("BudgetLimit/BudgetValue = %d/%d, want 4/5 (the retry attempt crossing the four in-budget first attempts)", ev.BudgetLimit, ev.BudgetValue)
	}
	if ev.Agent != "researcher" {
		t.Errorf("BudgetExhausted.Agent = %q, want the delegated child agent", ev.Agent)
	}
	if ev.ChildRunID == "" {
		t.Error("BudgetExhausted must attribute the offending child run")
	}
	if strings.TrimSpace(ev.Reason) == "" {
		t.Error("BudgetExhausted carried no reason text")
	}

	// The delegate results say exactly what the budget allowed: the two
	// healthy children report success, both budget-cancelled children
	// fail with the honest aggregate budget note.
	type delegateResult struct {
		finish  bool
		content string
	}
	results := map[string]delegateResult{}
	for _, e := range all {
		if e.Type != EventToolFinish && e.Type != EventToolFailed {
			continue
		}
		if e.ToolResult == nil || e.ToolResult.Name != "delegate" {
			continue
		}
		results[e.ToolResult.ToolCallID] = delegateResult{
			finish:  e.Type == EventToolFinish,
			content: e.ToolResult.Content,
		}
	}
	if len(results) != 4 {
		t.Fatalf("delegate tool results = %d (%v), want 4", len(results), results)
	}
	for _, tc := range []struct {
		callID   string
		finish   bool
		wantText string
	}{
		{"fan-call-a", true, "fan-a artifacts merged"},
		{"fan-call-c", true, "fan-c artifacts merged"},
		{"fan-call-b", false, "aggregate child-turn budget exhausted"},
		{"fan-call-d", false, "aggregate child-turn budget exhausted"},
	} {
		got, ok := results[tc.callID]
		if !ok {
			t.Fatalf("delegate %s emitted no tool result", tc.callID)
		}
		if got.finish != tc.finish {
			t.Errorf("delegate %s finished=%v, want %v (content %q)", tc.callID, got.finish, tc.finish, got.content)
		}
		if !strings.Contains(got.content, tc.wantText) {
			t.Errorf("delegate %s content %q does not contain %q", tc.callID, got.content, tc.wantText)
		}
	}

	// Run-scoped provider isolation: the parent instance served only the
	// parent turns; every clone served exactly one child task; the four
	// clone ledgers stayed pairwise distinct.
	calls, dead, plog := parent.snapshot()
	if len(calls) != 1 || calls[fanParentIdentity] != 2 {
		t.Fatalf("parent provider instance served %v, want exactly the two parent turns (%v)", calls, plog)
	}
	if dead != 0 {
		t.Fatalf("parent run saw %d dead-context calls, want 0", dead)
	}
	clones := scripts.registeredClones()
	if len(clones) != 4 {
		t.Fatalf("CloneForRun produced %d run-scoped clones, want one per sibling child", len(clones))
	}
	cloneIDs := map[int64]bool{}
	served := map[string]bool{}
	for _, cl := range clones {
		cloneIDs[cl.instanceID] = true
		ccalls, cdead, clog := cl.snapshot()
		if len(ccalls) != 1 {
			t.Fatalf("clone id%d served %v, want exactly one child task (%v)", cl.instanceID, ccalls, clog)
		}
		var task string
		for identity := range ccalls {
			task = identity
		}
		if served[task] {
			t.Fatalf("task %q served by more than one provider instance: mutable run state leaked across runs", task)
		}
		served[task] = true
		switch task {
		case "fan-a", "fan-c":
			if ccalls[task] != 1 || cdead != 0 {
				t.Fatalf("healthy child %s: clone ledger %v (dead calls %d), want 1 live scripted call", task, ccalls, cdead)
			}
		case "fan-b", "fan-d":
			if ccalls[task] != 2 {
				t.Fatalf("retrying child %s: clone served %d calls, want exactly its two attempts (no charge or attempt after cancellation) (%v)", task, ccalls[task], clog)
			}
			if cdead != 1 {
				t.Fatalf("retrying child %s: %d calls on a dead context, want exactly the boundary retry attempt (attempt 1 delivered its transient error with a live context)", task, cdead)
			}
		default:
			t.Fatalf("clone id%d served unexpected child identity %q", cl.instanceID, task)
		}
	}
	if cloneIDs[0] {
		t.Fatal("a clone reused the parent provider instance")
	}
	if len(cloneIDs) != 4 {
		t.Fatalf("clones must carry distinct run-scoped instance ids: %v", cloneIDs)
	}
}
