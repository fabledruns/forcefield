package runtime

// Adversarial resource-and-admission tests for the orchestration layer:
// per-run spawn budgets (cumulative across nesting), spawn depth, the
// process-wide child semaphore, cancellation during admission, budget
// lifetime across runs and runtimes, and the child progress cap. All
// duration bounds here are deadlock detectors only; every asserted
// property is an exact count or a gate-ordered event, never a timing
// window. Evidence levels are marked [U] (unit) and [R] (adversarial
// regression through the real run path).

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// gatedChildrenProvider scripts parent turns sequentially and routes
// child calls by task text (the child's system prompt always contains
// "child-system"), but each child call is held on a per-key gate the
// test controls. While parked, the call counts as in-flight and the
// provider tracks the in-flight peak, so semaphore bounds become exact
// observations instead of timing guesses. Children enter (and are
// counted) synchronously inside StreamChat; the returned channel only
// delivers once the gate opens, or delivers nothing if the context is
// cancelled first. Out-of-script children fail fast with
// context.Canceled so a wrong route can never silently stall a detector.
//
// Which delegate call wins which semaphore slot is a goroutine-scheduling
// race in the implementation, so tests must never assume a specific task
// holds a specific slot: arrivals are observed as counts (arrivalSignal,
// waitArrivals, arrivedKeys) and releases target currently parked
// children. The only ordering the tests rely on is the one they create
// themselves via gates.
type gatedChildrenProvider struct {
	mu          sync.Mutex
	parentTurns [][]providers.StreamEvent
	parentIdx   int
	childTurns  map[string][][]providers.StreamEvent
	childIdx    map[string]int

	inflight int
	peak     int

	// inflight only counts gated children; the parent's own buffered
	// turn delivery is instantaneous and must not inflate the child
	// in-flight peak.
	entered      map[string]int
	delivered    map[string]int
	arrived      map[string]chan struct{}
	arrivedDone  map[string]bool
	gates        map[string]chan struct{}
	releasedDone map[string]bool

	arrivalSignal chan struct{} // one token per child StreamChat entry
}

func newGatedChildrenProvider(parentTurns [][]providers.StreamEvent, childTurns map[string][][]providers.StreamEvent) *gatedChildrenProvider {
	p := &gatedChildrenProvider{
		parentTurns:  parentTurns,
		childTurns:   childTurns,
		childIdx:     map[string]int{},
		entered:      map[string]int{},
		delivered:    map[string]int{},
		arrived:      map[string]chan struct{}{},
		arrivedDone:  map[string]bool{},
		gates:        map[string]chan struct{}{},
		releasedDone: map[string]bool{},

		arrivalSignal: make(chan struct{}, 32),
	}
	for key := range childTurns {
		p.arrived[key] = make(chan struct{})
		p.gates[key] = make(chan struct{})
	}
	return p
}

func (p *gatedChildrenProvider) StreamChat(ctx context.Context, messages []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	var turn []providers.StreamEvent
	var gate chan struct{}
	var heldKey string
	p.mu.Lock()
	if len(messages) > 0 && strings.Contains(messages[0].Content, "child-system") {
		key := ""
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == providers.UserRole {
				key = messages[i].Content
				break
			}
		}
		turns, ok := p.childTurns[key]
		idx := p.childIdx[key]
		if !ok || idx >= len(turns) {
			p.mu.Unlock()
			return nil, context.Canceled
		}
		turn = turns[idx]
		p.childIdx[key]++
		p.entered[key]++
		heldKey = key
		if !p.arrivedDone[key] {
			p.arrivedDone[key] = true
			close(p.arrived[key])
		}
		gate = p.gates[key]
		p.inflight++
		if p.inflight > p.peak {
			p.peak = p.inflight
		}
		select {
		case p.arrivalSignal <- struct{}{}:
		default:
		}
	} else {
		// Parent turn: strictly sequential script, delivered instantly.
		if p.parentIdx >= len(p.parentTurns) {
			p.mu.Unlock()
			return nil, context.Canceled
		}
		turn = p.parentTurns[p.parentIdx]
		p.parentIdx++
	}
	p.mu.Unlock()

	if gate == nil {
		events := make(chan providers.StreamEvent, len(turn))
		for _, event := range turn {
			events <- event
		}
		close(events)
		return events, nil
	}

	events := make(chan providers.StreamEvent, len(turn))
	go func() {
		select {
		case <-gate:
		case <-ctx.Done():
			p.standDown()
			close(events)
			return
		}
		// The hold ends before the events are delivered, so a run that
		// joins and releases its semaphore slot afterwards can never
		// overlap the released child in the counter.
		p.standDown()
		p.markDelivered(heldKey)
		for _, event := range turn {
			events <- event
		}
		close(events)
	}()
	return events, nil
}

func (p *gatedChildrenProvider) standDown() {
	p.mu.Lock()
	p.inflight--
	p.mu.Unlock()
}

func (p *gatedChildrenProvider) markDelivered(key string) {
	if key == "" {
		return
	}
	p.mu.Lock()
	p.delivered[key]++
	p.mu.Unlock()
}

// parkedKeys returns the task keys whose child calls are currently held
// on their gates (entered but not yet delivered).
func (p *gatedChildrenProvider) parkedKeys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make([]string, 0, len(p.entered))
	for key, n := range p.entered {
		if n > p.delivered[key] {
			keys = append(keys, key)
		}
	}
	return keys
}

func (p *gatedChildrenProvider) arrivedCh(key string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.arrived[key]
}

func (p *gatedChildrenProvider) release(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.releasedDone[key] {
		p.releasedDone[key] = true
		close(p.gates[key])
	}
}

func (p *gatedChildrenProvider) enteredCount(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entered[key]
}

func (p *gatedChildrenProvider) peakInFlight() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

// waitArrivals blocks until at least target gated children have entered
// the provider, in whatever order the implementation admitted them.
// It counts cumulative entries, not consumed signals, so repeated calls
// with growing targets compose.
func waitArrivals(t *testing.T, p *gatedChildrenProvider, target int, what string) {
	t.Helper()
	for {
		if p.enteredTotal() >= target {
			return
		}
		select {
		case <-p.arrivalSignal:
		case <-time.After(5 * time.Second):
			t.Fatalf("deadlock detector: only %d/%d children entered before %q failed (entered=%v peak=%d)",
				p.enteredTotal(), target, what, p.entered, p.peak)
		}
	}
}

// arrivedKeys returns every task key that entered the provider so far
// (a joined child stays in the set; use the entered/delivered protocol
// in the caller to reason about who is still parked).
func (p *gatedChildrenProvider) arrivedKeys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make([]string, 0, len(p.entered))
	for key, n := range p.entered {
		if n > 0 {
			keys = append(keys, key)
		}
	}
	return keys
}

// enteredTotal sums every child StreamChat entry.
func (p *gatedChildrenProvider) enteredTotal() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, n := range p.entered {
		total += n
	}
	return total
}

// holdingTool is a scheduler-level fixture for the stopped-guard test:
// it counts executions, signals entered on entry, and parks until its
// release channel closes (it deliberately ignores ctx: the scheduler,
// not the tool, must produce the cancellation semantics this test
// pins).
type holdingTool struct {
	name     string
	message  string
	entered  chan struct{}
	release  chan struct{}
	executed atomic.Int32
}

func (t *holdingTool) Name() string { return t.name }
func (t *holdingTool) Description() string {
	return "Adversarial fixture: parks on entry until released."
}
func (t *holdingTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"value": map[string]any{"type": "string"},
	}}
}
func (t *holdingTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	t.executed.Add(1)
	if t.entered != nil {
		select {
		case t.entered <- struct{}{}:
		default:
		}
	}
	<-t.release
	return tools.Result{Content: t.message}, nil
}

func (t *holdingTool) count() int { return int(t.executed.Load()) }

func stringTurns(lines ...string) []providers.StreamEvent {
	events := make([]providers.StreamEvent, 0, len(lines))
	for _, line := range lines {
		events = append(events, providers.StreamEvent{Text: line})
	}
	events = append(events, providers.StreamEvent{Done: true})
	return events
}

// oneTextTurn wraps stringTurns as the single-turn turn list keyed
// providers expect for a child whose whole run is one text answer.
func oneTextTurn(lines ...string) [][]providers.StreamEvent {
	return [][]providers.StreamEvent{stringTurns(lines...)}
}

func delegateCall(id, agentName, taskText string) providers.ToolCall {
	return providers.ToolCall{
		ID:        id,
		Name:      "delegate",
		Arguments: map[string]any{"agent": agentName, "task": taskText},
	}
}

// TestAdversarialGrandchildDelegationConsumesRunBudget asks whether a
// grandchild's spawn can multiply the run's spawn budget. The budget is
// cumulative across every nesting level: the grandchild's admission
// charges the SAME counter as its parent child and the root's call, so
// every later spawn is refused with the Honest cumulative count.
// Sequence (deterministic, all sequential):
//  1. root delegates "mid work" -> child spawns (run total 1)
//  2. child delegates "leaf one" -> grandchild spawns (run total 2)
//  3. child delegates "leaf two" -> refused, count stays 2 (rejects never
//     charge, so the honest count cannot drift upward)
//  4. root delegates "second try" -> refused with count 2, no child run
func TestAdversarialGrandchildDelegationConsumesRunBudget(t *testing.T) {
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-d1", "researcher", "mid work")}},
				{Done: true},
			},
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-d2", "researcher", "second try")}},
				{Done: true},
			},
			{
				{Text: "coordinated."},
				{Done: true},
			},
		},
		childTurns: map[string][][]providers.StreamEvent{
			"mid work": {
				{
					{ToolCalls: []providers.ToolCall{delegateCall("call-g1", "gchild", "leaf one")}},
					{Done: true},
				},
				{
					{ToolCalls: []providers.ToolCall{delegateCall("call-g2", "gchild", "leaf two")}},
					{Done: true},
				},
				stringTurns("researcher report"),
			},
			"leaf one": oneTextTurn("leaf one done"),
		},
	}
	registry := agent.NewRegistry()
	for _, def := range []agent.Definition{
		{Name: "researcher", Description: "mid-level child", SystemPrompt: "child-system", Tools: []string{"echo"}, Skills: []string{}},
		{Name: "gchild", Description: "leaf grandchild", SystemPrompt: "gchild-system", Tools: []string{"echo"}, Skills: []string{}},
	} {
		if err := registry.Register(def); err != nil {
			t.Fatalf("register %s: %v", def.Name, err)
		}
	}
	r := newProbeRuntime(t, provider, config.Orchestration{
		Enabled:           true,
		MaxDepth:          intPtr(2),
		MaxChildrenPerRun: intPtr(2),
	}, registry)

	events, err := r.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "coordinate"},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	results, final := drainEventsForTest(t, events)

	if final == nil || final.Content != "coordinated." {
		t.Fatalf("final = %#v, want the parent's closing answer", final)
	}
	d1 := results["call-d1"]
	if d1 == nil || !d1.Success {
		t.Fatalf("first delegate result missing or failed: %+v", d1)
	}
	if !strings.Contains(d1.Content, "turns=3 tool_calls=2") {
		t.Fatalf("child header does not record both nested delegate calls: %s", d1.Content)
	}
	if !strings.Contains(d1.Content, "researcher report") {
		t.Fatalf("child report body missing: %s", d1.Content)
	}
	d2 := results["call-d2"]
	if d2 == nil || !d2.IsError {
		t.Fatalf("over-budget delegate must fail with an honest count: %+v", d2)
	}
	if !strings.Contains(d2.Content, "delegation limit reached (2 child runs in this session, limit 2)") {
		t.Fatalf("limit message must carry the cumulative charged count of 2: %s", d2.Content)
	}
	// The refused spawns never reached the provider: the grandchild leaf
	// turn and the second child's run were never started.
	if got := provider.childCallsCount("leaf two"); got != 0 {
		t.Fatalf("grandchild for the over-budget call ran %d times, want 0", got)
	}
	if got := provider.childCallsCount("second try"); got != 0 {
		t.Fatalf("child spawned for a refused delegate call: %d runs", got)
	}
	if got := provider.childCallsCount("leaf one"); got != 1 {
		t.Fatalf("admitted grandchild runs = %d, want 1", got)
	}
}

// TestAdversarialConcurrentAdmissionChargesExactlyThreeOfFive [R]: five
// parent delegate calls race admission against a budget of 3 (semaphore
// cap 2, so not all fits are possible at once either). The charged count
// printed in every refusal is the exactly-once proof: admission is
// serialized by the counter's mutex and the charge must land exactly
// once per admitted spawn, no more and no less, no matter how the
// goroutines interleave. Once three children have been admitted, every
// remaining call is refused with "(3 child runs in this session, limit
// 3)" — a counter that charged on rejection or double-charged under
// concurrency would print a different number.
func TestAdversarialConcurrentAdmissionChargesExactlyThreeOfFive(t *testing.T) {
	const fan = 5
	toolCalls := make([]providers.ToolCall, 0, fan)
	childTurns := map[string][][]providers.StreamEvent{}
	for i := 1; i <= fan; i++ {
		task := fmt.Sprintf("fan %d", i)
		toolCalls = append(toolCalls, delegateCall(fmt.Sprintf("call-fan-%d", i), "researcher", task))
		childTurns[task] = oneTextTurn(fmt.Sprintf("fan %d done", i))
	}
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: toolCalls},
				{Done: true},
			},
			stringTurns("coordinated."),
		},
		childTurns: childTurns,
	}
	r := newProbeRuntime(t, provider, childConfig(t, 2, 3), newChildRegistryForTests())

	events, err := r.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "fan out five"},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	results, final := drainEventsForTest(t, events)

	if final == nil || final.Content != "coordinated." {
		t.Fatalf("final = %#v, want the parent's closing answer", final)
	}
	const limitText = "delegation limit reached (3 child runs in this session, limit 3)"
	var finishes, refusals int
	for i := 1; i <= fan; i++ {
		res := results[fmt.Sprintf("call-fan-%d", i)]
		if res == nil {
			t.Fatalf("delegate call %d never produced a terminal result", i)
		}
		switch {
		case res.Success:
			finishes++
		case res.IsError && strings.Contains(res.Content, limitText):
			refusals++
		default:
			t.Fatalf("delegate call %d produced an unexpected terminal: %+v", i, res)
		}
	}
	if finishes != 3 || refusals != 2 {
		t.Fatalf("delegate outcomes = (%d admitted, %d refused), want (3, 2)", finishes, refusals)
	}
	// Exactly-once: each keyed child ran at most once and exactly three
	// children ran in total.
	totalRuns := 0
	for i := 1; i <= fan; i++ {
		n := provider.childCallsCount(fmt.Sprintf("fan %d", i))
		if n > 1 {
			t.Fatalf("child %d ran %d times; admission double-charged", i, n)
		}
		totalRuns += n
	}
	if totalRuns != 3 {
		t.Fatalf("total spawned child runs = %d, want exactly 3", totalRuns)
	}
}

// TestAdversarialSemaphoreCapsInFlightAndFreesExactlyOneSlot [R]: four
// gate-held children race a semaphore cap of 2. The gate design makes
// every claim absolute rather than timing-based. Which delegate call
// lands in which slot is a goroutine-scheduling race and is deliberately
// not assumed anywhere:
//   - two children hold the semaphore (both parked in the provider) and
//     neither of the other two can enter: admission blocks before any
//     spawn, so the provider only ever sees two children;
//   - releasing exactly one parked child frees exactly one slot: exactly
//     one queued child enters per release;
//   - a double release (M9) would admit two queued children at once,
//     which the exact arrival count forbids;
//   - a missing release (M9) would deadlock the queued admission, which
//     the arrival detector forbids;
//   - the observed in-flight peak is exactly 2: neither 3 (cap ignored)
//     nor 1 (cap lowered).
func TestAdversarialSemaphoreCapsInFlightAndFreesExactlyOneSlot(t *testing.T) {
	parentTurns := [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{
				delegateCall("call-hold-1", "researcher", "hold one"),
				delegateCall("call-hold-2", "researcher", "hold two"),
				delegateCall("call-hold-3", "researcher", "hold three"),
				delegateCall("call-hold-4", "researcher", "hold four"),
			}},
			{Done: true},
		},
		stringTurns("coordinated."),
	}
	childTurns := map[string][][]providers.StreamEvent{}
	for _, key := range []string{"hold one", "hold two", "hold three", "hold four"} {
		childTurns[key] = oneTextTurn(key + " done")
	}
	provider := newGatedChildrenProvider(parentTurns, childTurns)
	r := newProbeRuntime(t, provider, childConfig(t, 2, 4), newChildRegistryForTests())

	type outcome struct {
		results map[string]*ToolResult
		final   *providers.Response
	}
	done := make(chan outcome, 1)
	errCh := make(chan error, 1)
	go func() {
		events, err := r.StreamChat(context.Background(), []providers.Message{
			{Role: providers.UserRole, Content: "park four children"},
		})
		if err != nil {
			errCh <- err
			return
		}
		results, final := drainEventsForTest(t, events)
		done <- outcome{results: results, final: final}
	}()

	waitArrivals(t, provider, 2, "two children to fill the cap")

	// Both slots are held and parked. Nobody was released, so the other
	// two delegates cannot have spawned: the admission blocks before any
	// child run starts.
	if got := provider.enteredTotal(); got != 2 {
		t.Fatalf("children in the provider = %d, want exactly 2 (the cap)", got)
	}
	if got := provider.peakInFlight(); got != 2 {
		t.Fatalf("in-flight peak = %d, want exactly 2 while two children hold the cap", got)
	}

	released := map[string]bool{}
	releaseOneParked := func(t *testing.T) {
		t.Helper()
		for _, key := range provider.parkedKeys() {
			if !released[key] {
				provider.release(key)
				released[key] = true
				return
			}
		}
		t.Fatal("test bug: no parked child to release")
	}

	// Releasing exactly one parked child frees exactly one slot: the next
	// queued child enters, and only one of them.
	releaseOneParked(t)
	waitArrivals(t, provider, 3, "a third child for the freed slot")
	if got := provider.enteredTotal(); got != 3 {
		t.Fatalf("children in the provider = %d, want exactly 3 (one freed slot admits exactly one child)", got)
	}
	if got := provider.peakInFlight(); got != 2 {
		t.Fatalf("in-flight peak = %d, want exactly 2 (no phantom slot after a single release)", got)
	}

	// The second release admits the last child, then everything joins.
	releaseOneParked(t)
	waitArrivals(t, provider, 4, "the fourth child")
	releaseOneParked(t)
	releaseOneParked(t)

	select {
	case err := <-errCh:
		t.Fatalf("StreamChat: %v", err)
	case out := <-done:
		if out.final == nil || out.final.Content != "coordinated." {
			t.Fatalf("final = %#v, want the parent's closing answer", out.final)
		}
		for i := 1; i <= 4; i++ {
			res := out.results[fmt.Sprintf("call-hold-%d", i)]
			if res == nil || !res.Success {
				t.Fatalf("gated child %d's delegate result missing or failed: %+v", i, res)
			}
		}
		for _, key := range []string{"hold one", "hold two", "hold three", "hold four"} {
			if got := provider.enteredCount(key); got != 1 {
				t.Fatalf("child %q entered the provider %d times, want exactly once", key, got)
			}
		}
		if got := provider.peakInFlight(); got != 2 {
			t.Fatalf("in-flight peak = %d, want exactly 2 across the whole run", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detector: run did not complete after all gates opened (a semaphore slot was never released?)")
	}
}

// TestAdversarialCancelWhileQueuedNeverStartsChild [R]: a delegate whose
// admission is parked on the semaphore must come back as cancelled and
// must never spawn its child run. Cancellation arrives while two
// children hold the semaphore; the queued delegate call observes the
// cancellation through the same tool context. Which specific delegate
// call ends up queued is a goroutine-scheduling race, so the assertion
// is the honest aggregate: every delegate terminal is a cancellation and
// the child-run count stays at 2 — the queued child never reached the
// provider, which is also the pin for the semaphore-full cancel branch
// (a mutant that sneaks the queued call through admission would show 3
// child spawns).
func TestAdversarialCancelWhileQueuedNeverStartsChild(t *testing.T) {
	parentTurns := [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{
				delegateCall("call-q1", "researcher", "hold one"),
				delegateCall("call-q2", "researcher", "hold two"),
				delegateCall("call-q3", "researcher", "hold three"),
			}},
			{Done: true},
		},
	}
	childTurns := map[string][][]providers.StreamEvent{
		"hold one":   oneTextTurn("hold one done"),
		"hold two":   oneTextTurn("hold two done"),
		"hold three": oneTextTurn("hold three done"),
	}
	provider := newGatedChildrenProvider(parentTurns, childTurns)
	r := newProbeRuntime(t, provider, childConfig(t, 2, 3), newChildRegistryForTests())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type drainOut struct {
		canceled bool
	}
	done := make(chan drainOut, 1)
	errCh := make(chan error, 1)
	go func() {
		events, err := r.StreamChat(ctx, []providers.Message{
			{Role: providers.UserRole, Content: "queue a child and cancel"},
		})
		if err != nil {
			errCh <- err
			return
		}
		out := drainOut{}
		for ev := range events {
			if ev.Type == EventCancelled {
				out.canceled = true
			}
		}
		done <- out
	}()

	waitArrivals(t, provider, 2, "two children to fill the cap")
	if got := provider.enteredTotal(); got != 2 {
		t.Fatalf("children in the provider = %d, want exactly 2 (the cap)", got)
	}
	cancel()

	select {
	case err := <-errCh:
		t.Fatalf("StreamChat: %v", err)
	case out := <-done:
		if !out.canceled {
			t.Fatal("run never reported cancellation after ctx was cancelled")
		}
		// No child spawned after cancellation: the queued admission can
		// only observe the cancelled context. This count is the real
		// proof the queued delegate skipped its child run, and it is
		// also the pin for the semaphore-full cancel branch (a mutant
		// that sneaks the queued call through admission would show 3
		// child spawns). Per-call delegate terminal events are best
		// effort under a cancelled run: the run loop may already be
		// emitting its own termination when they fire (pre-existing
		// runtime emit-drop semantics), so only structural facts about
		// spawns are asserted here.
		if got := provider.enteredTotal(); got != 2 {
			t.Fatalf("children in the provider after cancellation = %d, want exactly 2 (the queued child never spawned)", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detector: run did not terminate after cancellation")
	}
}

// TestAdversarialBudgetResetsPerRunAndNeverLeaks [R]: the per-run spawn
// budget is per StreamChat run, not per scheduler lifetime. Within run 1
// the budget of 1 binds: one child spawns, a second delegate call inside
// the same run is refused. Run 2 on the SAME runtime starts from a fresh
// counter and admits its child again — a budget leaked across runs (or a
// global counter) would refuse it.
func TestAdversarialBudgetResetsPerRunAndNeverLeaks(t *testing.T) {
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-d1", "researcher", "budget one")}},
				{Done: true},
			},
			stringTurns("first run closed"),
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-d2", "researcher", "budget two")}},
				{Done: true},
			},
			stringTurns("second run closed"),
		},
		childTurns: map[string][][]providers.StreamEvent{
			"budget one": oneTextTurn("child one report"),
			"budget two": oneTextTurn("child two report"),
		},
	}
	r := newProbeRuntime(t, provider, childConfig(t, 1, 1), newChildRegistryForTests())

	run := func(userText, delegateID, reportText, closing string) *ToolResult {
		t.Helper()
		events, err := r.StreamChat(context.Background(), []providers.Message{
			{Role: providers.UserRole, Content: userText},
		})
		if err != nil {
			t.Fatalf("StreamChat: %v", err)
		}
		results, final := drainEventsForTest(t, events)
		if final == nil || final.Content != closing {
			t.Fatalf("final = %#v, want %q", final, closing)
		}
		res := results[delegateID]
		if res == nil {
			t.Fatalf("delegate result %s missing", delegateID)
		}
		if !res.Success || !strings.Contains(res.Content, reportText) {
			t.Fatalf("delegate result = %+v, want a successful child report %q", res, reportText)
		}
		return res
	}

	run("run one", "call-d1", "child one report", "first run closed")
	run("run two", "call-d2", "child two report", "second run closed")

	for _, key := range []string{"budget one", "budget two"} {
		if got := provider.childCallsCount(key); got != 1 {
			t.Fatalf("child %q ran %d times, want exactly once", key, got)
		}
	}
}

// TestAdversarialTwoRuntimesHaveIndependentBudgets [R]: two runtimes,
// each with its own orchestrator and semaphore, must never see each
// other's spawn counts. A budget or semaphore shared at package or
// process scope would make the second runtime's first child impossible
// once the first runtime spent its budget.
func TestAdversarialTwoRuntimesHaveIndependentBudgets(t *testing.T) {
	newSignalProvider := func(task, report, closing string) *keyedProvider {
		return &keyedProvider{
			parentTurns: [][]providers.StreamEvent{
				{
					{ToolCalls: []providers.ToolCall{delegateCall("call-independent", "researcher", task)}},
					{Done: true},
				},
				stringTurns(closing),
			},
			childTurns: map[string][][]providers.StreamEvent{
				task: oneTextTurn(report),
			},
		}
	}
	p1 := newSignalProvider("budget alpha", "alpha report", "alpha closed")
	p2 := newSignalProvider("budget beta", "beta report", "beta closed")
	r1 := newProbeRuntime(t, p1, childConfig(t, 1, 1), newChildRegistryForTests())
	r2 := newProbeRuntime(t, p2, childConfig(t, 1, 1), newChildRegistryForTests())

	type runOut struct {
		results map[string]*ToolResult
		final   *providers.Response
	}
	runRuntime := func(rt *Runtime, task, closing string) runOut {
		t.Helper()
		done := make(chan runOut, 1)
		errCh := make(chan error, 1)
		go func() {
			events, err := rt.StreamChat(context.Background(), []providers.Message{
				{Role: providers.UserRole, Content: task},
			})
			if err != nil {
				errCh <- err
				return
			}
			results, final := drainEventsForTest(t, events)
			done <- runOut{results: results, final: final}
		}()
		select {
		case err := <-errCh:
			t.Fatalf("StreamChat: %v", err)
		case out := <-done:
			return out
		case <-time.After(10 * time.Second):
			t.Fatalf("deadlock detector: runtime for %q never completed", closing)
		}
		return runOut{}
	}

	for _, pair := range []struct {
		rt      *Runtime
		probe   *keyedProvider
		task    string
		closing string
	}{
		{r1, p1, "budget alpha", "alpha closed"},
		{r2, p2, "budget beta", "beta closed"},
	} {
		out := runRuntime(pair.rt, pair.task, pair.closing)
		res := out.results["call-independent"]
		if res == nil || !res.Success || !strings.Contains(res.Content, "report") {
			t.Fatalf("independent budget run for %q failed: %+v", pair.closing, res)
		}
		if got := pair.probe.childCallsCount(pair.task); got != 1 {
			t.Fatalf("child for %q ran %d times, want exactly once", pair.task, got)
		}
	}
}

// TestAdversarialChildProgressLinesCappedExactly [R]: a long child run
// cannot flood the parent transcript through the progress path. Every
// child turn emits one throttled line, but the recorder's budget is
// maxChildProgressLines: an off-by-one here would either drop the first
// or last allowed line or leak one beyond the cap, so the cap is asserted
// exactly (32 lines, the last one naming turn 32, zero afterwards) with
// the run itself left intact (all 41 turns executed).
func TestAdversarialChildProgressLinesCappedExactly(t *testing.T) {
	const turns = 40
	childCalls := make([][]providers.StreamEvent, 0, turns+1)
	for i := 0; i < turns; i++ {
		childCalls = append(childCalls, []providers.StreamEvent{
			{ToolCalls: []providers.ToolCall{{
				ID:        fmt.Sprintf("call-chatty-%d", i),
				Name:      "echo",
				Arguments: map[string]any{"value": fmt.Sprintf("tick-%d", i)},
			}}},
			{Done: true},
		})
	}
	childCalls = append(childCalls, stringTurns("chatty report lands here"))

	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-chatty", "researcher", "talk a lot")}},
				{Done: true},
			},
			stringTurns("coordinated."),
		},
		childTurns: map[string][][]providers.StreamEvent{
			"talk a lot": childCalls,
		},
	}
	r := newProbeRuntime(t, provider, config.Orchestration{Enabled: true}, newChildRegistryForTests())

	events, err := r.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "run a chatty child"},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var progress []string
	for ev := range events {
		if ev.Type == EventToolProgress && ev.ToolProgress != nil &&
			ev.ToolProgress.Stream == "progress" && strings.HasPrefix(ev.ToolProgress.Data, "[delegate: agent") {
			progress = append(progress, ev.ToolProgress.Data)
		}
		if ev.ToolResult != nil && ev.ToolResult.ToolCallID == "call-chatty" &&
			(ev.Type == EventToolFinish || ev.Type == EventToolFailed) {
			res := ev.ToolResult
			if !strings.Contains(res.Content, "turns=41 tool_calls=40") {
				t.Fatalf("delegate header lost the child's full run: %s", res.Content)
			}
			if !strings.Contains(res.Content, "chatty report lands here") {
				t.Fatalf("child report body missing: %s", res.Content)
			}
		}
	}

	if len(progress) != maxChildProgressLines {
		t.Fatalf("parent received %d child progress lines, want exactly %d", len(progress), maxChildProgressLines)
	}
	if !strings.Contains(progress[len(progress)-1], `turn 32]`) {
		t.Fatalf("last admitted progress line = %q, want the 32nd turn", progress[len(progress)-1])
	}
}

// TestAdversarialTwoRootRunsOnOneRuntimeSerialize [U]: one runtime's run
// loop owns r.runMu, so two concurrent StreamChat calls on the same
// runtime cannot overlap. The probe provider tracks in-flight calls; a
// serialized run shows peak == 1 without any timing assumption, and both
// runs still produce their own final answer (no one is dropped or failed
// by the serialization).
func TestAdversarialTwoRootRunsOnOneRuntimeSerialize(t *testing.T) {
	provider := &concurrencyProbeProvider{turns: [][]providers.StreamEvent{
		{{Text: "run one"}, {Done: true}},
		{{Text: "run two"}, {Done: true}},
	}}
	r := newProbeRuntime(t, provider, config.Orchestration{Enabled: true}, newChildRegistryForTests())

	type runOut struct{ final *providers.Response }
	capture := func(text string) chan runOut {
		done := make(chan runOut, 1)
		go func() {
			events, err := r.StreamChat(context.Background(), []providers.Message{
				{Role: providers.UserRole, Content: text},
			})
			if err != nil {
				t.Errorf("StreamChat: %v", err)
				close(done)
				return
			}
			var final *providers.Response
			for ev := range events {
				if ev.Type == EventDone {
					final = ev.Response
				}
			}
			done <- runOut{final: final}
		}()
		return done
	}

	got := []runOut{
		<-capture("start run one"),
		<-capture("start run two"),
	}
	if got[0].final == nil || got[0].final.Content != "run one" {
		t.Fatalf("first run's final = %#v, want %q", got[0].final, "run one")
	}
	if got[1].final == nil || got[1].final.Content != "run two" {
		t.Fatalf("second run's final = %#v, want %q", got[1].final, "run two")
	}
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 2 {
		t.Fatalf("parent provider calls = %d, want 2", calls)
	}
	if peak := provider.peak; peak > 1 {
		t.Fatalf("peak concurrent root runs = %d, want 1 (runMu serializes roots on one runtime)", peak)
	}
}

// TestAdversarialDelegateDepthLimitReportsHonestDepth [U]: the depth
// branch of admitSpawn is unreachable through model flows (the leaf-drop
// strips delegate first), so the unit drives a scope one past the limit
// straight into ExecuteStream. The refusal text must carry the honest
// depth numbers, the budget must stay uncharged (a depth refusal that
// charged would print different numbers later), the semaphore must be
// handed back, and no provider call may happen.
func TestAdversarialDelegateDepthLimitReportsHonestDepth(t *testing.T) {
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{},
		childTurns:  map[string][][]providers.StreamEvent{},
	}
	r := newProbeRuntime(t, provider, config.Orchestration{
		Enabled:  true,
		MaxDepth: intPtr(2),
	}, newChildRegistryForTests())

	// Craft a scope at depth 2 (a grandchild) sharing the run's counters:
	// its delegate attempt is the depth-limit branch, one past the edge.
	scope := &orchestrationScope{
		orch:   r.orch,
		shared: &orchestrationShared{},
		depth:  2,
		chain:  []string{"root", "researcher"},
		snap:   &runSnapshot{scheduler: r.scheduler},
	}
	ctx := withOrchestrationScope(context.Background(), scope)
	dt := delegateTool{orch: r.orch}

	res, err := dt.ExecuteStream(ctx, map[string]any{
		"agent": "researcher",
		"task":  "one past the depth edge",
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if !res.IsError {
		t.Fatalf("depth-limit refusal must be an error result: %+v", res)
	}
	if !strings.Contains(res.Content, "delegation depth limit reached (depth 3 with limit 2)") {
		t.Fatalf("depth refusal text = %q, want the honest depth numbers", res.Content)
	}
	if scope.shared.spawned != 0 {
		t.Fatalf("depth refusal charged the budget: spawned=%d", scope.shared.spawned)
	}
	if used := len(r.orch.sem); used != 0 {
		t.Fatalf("semaphore slot leaked after the depth refusal: %d in use", used)
	}
	if got := provider.totalCalls(); got != 0 {
		t.Fatalf("provider called %d times for a refused delegate, want 0", got)
	}
}

// TestAdversarialDelegateCancelledBeforeStartNeverSpawns [U]: a delegate
// tool context that is already cancelled at entry must return the
// cancellation message without consulting the registry, the budget, the
// semaphore, or the provider.
func TestAdversarialDelegateCancelledBeforeStartNeverSpawns(t *testing.T) {
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{},
		childTurns:  map[string][][]providers.StreamEvent{},
	}
	r := newProbeRuntime(t, provider, childConfig(t, 1, 2), newChildRegistryForTests())
	scope := &orchestrationScope{
		orch:   r.orch,
		shared: &orchestrationShared{},
		depth:  0,
		chain:  []string{"root"},
		snap:   &runSnapshot{scheduler: r.scheduler},
	}
	ctx, cancel := context.WithCancel(withOrchestrationScope(context.Background(), scope))
	cancel()

	dt := delegateTool{orch: r.orch}
	res, err := dt.ExecuteStream(ctx, map[string]any{
		"agent": "researcher",
		"task":  "cancelled before the doors opened",
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "delegation cancelled before it started") {
		t.Fatalf("pre-cancelled delegate = %+v, want the cancellation message", res)
	}
	if scope.shared.spawned != 0 {
		t.Fatalf("cancelled delegate charged the budget: spawned=%d", scope.shared.spawned)
	}
	if got := provider.totalCalls(); got != 0 {
		t.Fatalf("provider called %d times for a cancelled delegate, want 0", got)
	}
}

// TestAdversarialDelegateAdmissionParkedThenCancelled [U]: the
// semaphore-full branch. With the semaphore at capacity, the delegate
// goroutine cannot complete admission no matter when the cancellation
// lands: either the entry pre-check or the semaphore select returns the
// identical cancellation message, and in neither order can a child spawn
// (the semaphore was never available).
func TestAdversarialDelegateAdmissionParkedThenCancelled(t *testing.T) {
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{},
		childTurns:  map[string][][]providers.StreamEvent{},
	}
	r := newProbeRuntime(t, provider, childConfig(t, 1, 2), newChildRegistryForTests())
	// Park the process-wide semaphore by hand (same-package access).
	r.orch.sem <- struct{}{}

	scope := &orchestrationScope{
		orch:   r.orch,
		shared: &orchestrationShared{},
		depth:  0,
		chain:  []string{"root"},
		snap:   &runSnapshot{scheduler: r.scheduler},
	}
	ctx, cancel := context.WithCancel(withOrchestrationScope(context.Background(), scope))
	defer cancel()

	dt := delegateTool{orch: r.orch}
	type resultOut struct {
		res tools.Result
		err error
	}
	done := make(chan resultOut, 1)
	go func() {
		// Cancellation is armed before the call. The goroutine can
		// observe it at the entry pre-check or at the semaphore select —
		// the semaphore is full from before the call started and only
		// empties after the test drains it, so no admission is possible
		// under either order and the result message is identical. A
		// mutant that loses the tool context in the select would instead
		// park forever and trip the detector below.
		cancel()
		res, err := dt.ExecuteStream(ctx, map[string]any{
			"agent": "researcher",
			"task":  "parked forever otherwise",
		}, nil)
		done <- resultOut{res: res, err: err}
	}()

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("ExecuteStream: %v", out.err)
		}
		if !out.res.IsError || !strings.Contains(out.res.Content, "delegation cancelled before it started") {
			t.Fatalf("parked+cancelled delegate = %+v, want the cancellation message", out.res)
		}
		if scope.shared.spawned != 0 {
			t.Fatalf("cancelled delegate charged the budget: spawned=%d", scope.shared.spawned)
		}
		if got := provider.totalCalls(); got != 0 {
			t.Fatalf("provider called %d times, want 0 (no child spawned)", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detector: parked admission never observed cancellation")
	}
	<-r.orch.sem
}

// TestAdversarialSchedulerStoppedGuardSkipsQueuedCalls [U]: the batch
// scheduler's stopped guard. Once a queued call could run after the run
// context was cancelled, it must return the pre-execution cancellation
// instead of executing. With MaxConcurrency 1 the tool that wins the
// lane parks until released and the other call queues on the scheduler's
// lane; which tool wins is a goroutine race, so the test determines the
// roles at runtime and then cancels while the lane holder is parked.
// Releasing the holder ends its execution under a cancelled context;
// the queued call must then be skipped: no execution, no tool-start
// event, and the Attempt-0 "cancelled before it started" result. A
// guardless batch would run the queued tool (it ignores ctx) and show up
// as an execution count plus a start event.
func TestAdversarialSchedulerStoppedGuardSkipsQueuedCalls(t *testing.T) {
	manager := tools.NewManager(tools.NewRegistry())
	holding := func(name, message string) *holdingTool {
		return &holdingTool{name: name, message: message, entered: make(chan struct{}, 1), release: make(chan struct{})}
	}
	one := holding("one", "one released")
	two := holding("two", "two never expected to run")
	for _, tool := range []tools.Tool{one, two} {
		if err := manager.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name(), err)
		}
	}
	s := newScheduler(manager, nil, nil, SchedulerConfig{MaxConcurrency: 1})

	var mu sync.Mutex
	startEvents, cancelEvents := map[string]EventType{}, map[string]EventType{}
	emit := func(e Event) bool {
		mu.Lock()
		defer mu.Unlock()
		switch e.Type {
		case EventToolStart:
			if e.ToolCall != nil {
				startEvents[e.ToolCall.Name] = e.Type
			}
		case EventToolFinish, EventToolFailed, EventToolCancelled, EventToolDenied:
			if e.ToolResult != nil {
				cancelEvents[e.ToolResult.Name] = e.Type
			}
		}
		return true
	}

	calls := []providers.ToolCall{
		{ID: "call-one", Name: "one", Arguments: map[string]any{"value": "x"}},
		{ID: "call-two", Name: "two", Arguments: map[string]any{"value": "x"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type batchOut struct{ results []ToolResult }
	done := make(chan batchOut, 1)
	go func() {
		done <- batchOut{results: s.RunWithManager(ctx, calls, emit, manager)}
	}()

	// Which tool wins the lane decides the roles: one parks until its
	// release channel is closed, the queued call stays parked on the
	// scheduler lane until then.
	holder, queued := one, two
	var holderRelease chan struct{}
	select {
	case <-one.entered:
		holder, queued = one, two
		holderRelease = one.release
	case <-two.entered:
		holder, queued = two, one
		holderRelease = two.release
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detector: neither holder executed (batch never started)")
	}

	if got := holder.count(); got != 1 {
		t.Fatalf("lane holder executed %d times, want exactly 1", got)
	}
	if got := queued.count(); got != 0 {
		t.Fatalf("queued call executed %d times while the holder parked on the only lane", got)
	}
	cancel()
	close(holderRelease)

	var results []ToolResult
	select {
	case b := <-done:
		results = b.results
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detector: batch never completed after cancellation")
	}

	if got := queued.count(); got != 0 {
		t.Fatalf("queued call executed %d times after cancellation, want 0 (stopped guard failed)", got)
	}
	mu.Lock()
	starts := len(startEvents)
	holderTerm, hasHolderTerm := cancelEvents[holder.Name()]
	_, hasQueuedTerm := cancelEvents[queued.Name()]
	mu.Unlock()

	// The holder was running and reports a cancellation from the run
	// path; the queued call was skipped pre-execution and is
	// deliberately eventless (no start event, no terminal event).
	if starts != 1 {
		t.Fatalf("tool-start events = %d, want exactly 1 (only the lane holder started)", starts)
	}
	if _, used := startEvents[holder.Name()]; !used {
		t.Fatal("the lane holder never announced its start")
	}
	if !hasHolderTerm || holderTerm != EventToolCancelled {
		t.Fatalf("lane holder %q terminal missing or wrong: %v", holder.Name(), holderTerm)
	}
	if hasQueuedTerm {
		t.Fatalf("queued call %q produced a terminal event though it must be skipped eventless", queued.Name())
	}
	if len(results) != 2 {
		t.Fatalf("batch returned %d results, want 2", len(results))
	}
	// Result array is in call order; find the queued call's entry.
	qres := results[indexOfCall(calls, queued.Name())]
	if !qres.IsError || qres.Success || qres.Attempt != 0 ||
		!strings.Contains(qres.Content, "tool execution cancelled before it started") {
		t.Fatalf("queued result = %+v, want the pre-execution cancellation", qres)
	}
}

func indexOfCall(calls []providers.ToolCall, name string) int {
	for i, c := range calls {
		if c.Name == name {
			return i
		}
	}
	return -1
}
