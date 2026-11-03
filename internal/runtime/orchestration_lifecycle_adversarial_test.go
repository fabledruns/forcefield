package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// Adversarial lifecycle tests for delegation joins and sibling
// attribution. The generic join semantics (child joins before the tool
// result, goroutine settle, duplicate-ID replay) are pinned in
// orchestration_lifecycle_test.go; the tests here cover the two
// parent-stream observables of the same invariants that no other test
// reaches: nothing child-derived may ever arrive after the delegate
// terminal result, and concurrent siblings' reports and branded progress
// lines must attribute to their own child only.

// newTwoAgentAdversarialRuntime wires the same production shape as
// newPermissionAdversarialRuntime but registers two child agents with
// distinct names, so delegate progress lines carry an attributable
// agent name and every sibling's accounting can be checked separately.
// delegate is allowed for the parent; children never need an operator.
func newTwoAgentAdversarialRuntime(t *testing.T, p providers.ModelProvider) *Runtime {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	echo := &countingEchoTool{}
	if err := full.Register(echo); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	registry := agent.NewRegistry()
	for _, name := range []string{"alpha", "beta"} {
		if err := registry.Register(agent.Definition{
			Name:         name,
			Description:  "test child agent " + name,
			SystemPrompt: "child-system",
			Tools:        []string{"echo"},
			Skills:       []string{},
		}); err != nil {
			t.Fatalf("register agent %s: %v", name, err)
		}
	}
	orch := newOrchestrator(
		config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive},
		registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
	}

	// Allow the child's own echo calls, keep the parent's delegate open;
	// everything else keeps the ask-by-default posture.
	rules := permissions.Rules{
		Default: permissions.Ask,
		Tools: map[string]permissions.Decision{
			"delegate": permissions.Allow,
			"echo":     permissions.Allow,
		},
	}
	store := newRecordingRulesStore(rules)
	manager := permissions.NewManagerWithRules(rules, store)

	r := &Runtime{
		provider:    p,
		agent:       agent.New("root", "root-system", ""),
		activeAgent: "root",
		manager:     full,
		fullManager: full,
		orch:        orch,
		scheduler:   newScheduler(full, manager, nil, DefaultSchedulerConfig),
	}
	orch.parent = r
	r.scheduler.askSerial = orch.childAskSerial()
	return r
}

// TestAdversarialNoChildProgressAfterDelegateTerminal [R]: join-before-
// return observed from the parent's stream. Every "[delegate: agent ...]"
// progress line the child produced must precede the delegate tool's
// terminal result event. A composition or join failure that lets the
// child keep streaming after the parent accepted its result would show
// up as a post-terminal line on the parent channel — structurally
// impossible with the unconditional join, so the property is timing-free.
func TestAdversarialNoChildProgressAfterDelegateTerminal(t *testing.T) {
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-quiet-join", "researcher", "quiet join")}},
				{Done: true},
			},
			stringTurns("parent read the report."),
		},
		// Three turns require tool calls (a text-turn ends the run), so
		// the child calls echo twice and reports on the third turn.
		childTurns: map[string][][]providers.StreamEvent{
			"quiet join": {
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-child-echo-a1",
						Name:      "echo",
						Arguments: map[string]any{"value": "a1"},
					}}},
					{Done: true},
				},
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-child-echo-a2",
						Name:      "echo",
						Arguments: map[string]any{"value": "a2"},
					}}},
					{Done: true},
				},
				stringTurns("child burst three"),
			},
		},
	}
	r := newProbeRuntime(t, provider, config.Orchestration{Enabled: true}, newChildRegistryForTests())

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "observe the quiet join",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	terminalAt := -1
	for i, ev := range drained {
		if ev.ToolResult != nil && ev.ToolResult.ToolCallID == "call-quiet-join" &&
			(ev.Type == EventToolFinish || ev.Type == EventToolFailed) {
			terminalAt = i
			break
		}
	}
	if terminalAt < 0 {
		t.Fatal("delegate terminal never reached the parent transcript")
	}
	if !strings.Contains(drained[terminalAt].ToolResult.Content, "child burst three") {
		t.Fatalf("terminal report body lost the child's final turn:\n%.300q", drained[terminalAt].ToolResult.Content)
	}
	if h := drained[terminalAt].ToolResult.Content; !strings.Contains(h, "turns=3 tool_calls=2") {
		t.Fatalf("delegate header lost the child's full run:\n%.200q", h)
	}

	// The child's full turn history, each line the recorder produced for
	// exactly one of the three turns, all strictly before the terminal.
	var before []string
	var after []string
	for i, ev := range drained {
		if ev.Type == EventToolProgress && ev.ToolProgress != nil &&
			ev.ToolProgress.Stream == "progress" && strings.HasPrefix(ev.ToolProgress.Data, "[delegate: agent") {
			if i < terminalAt {
				before = append(before, ev.ToolProgress.Data)
			} else {
				after = append(after, ev.ToolProgress.Data)
			}
		}
	}
	if len(after) != 0 {
		t.Fatalf("child progress reported after the delegate terminal - join-before-return broken: %v", after)
	}
	if len(before) != 3 {
		t.Fatalf("progress lines before terminal = %d (%v), want one per child turn (3)", len(before), before)
	}
}

// TestAdversarialSiblingReportsAttributeTheirOwnTurns [R]: two sibling
// children with different turn counts run from one parent turn. Each
// sibling's report carries its own accounting and body cross-checked
// against its own scripted turns only - a crossed or shared recorder
// would show beta's count or text inside alpha's report or vice versa.
// Progress lines keyed by agent name count exactly one line per turn and
// arrive in per-agent order 1..N.
func TestAdversarialSiblingReportsAttributeTheirOwnTurns(t *testing.T) {
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{
					delegateCall("call-alpha", "alpha", "alpha leg"),
					delegateCall("call-beta", "beta", "beta leg"),
				}},
				{Done: true},
			},
			stringTurns("parent coordinated both legs."),
		},
		// Alpha: one text turn, no tools. Beta: three turns (two echo
		// calls with distinct arguments, then the report text).
		childTurns: map[string][][]providers.StreamEvent{
			"alpha leg": {stringTurns("alpha marker report A11 done.")},
			"beta leg": {
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-child-echo-b1",
						Name:      "echo",
						Arguments: map[string]any{"value": "b1"},
					}}},
					{Done: true},
				},
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-child-echo-b2",
						Name:      "echo",
						Arguments: map[string]any{"value": "b2"},
					}}},
					{Done: true},
				},
				stringTurns("beta burst three. B33"),
			},
		},
	}
	r := newTwoAgentAdversarialRuntime(t, provider)

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "watch two legs run",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	results := map[string]*ToolResult{}
	for _, ev := range drained {
		if ev.ToolResult != nil && (ev.Type == EventToolFinish || ev.Type == EventToolFailed) {
			results[ev.ToolResult.ToolCallID] = ev.ToolResult
		}
	}
	alpha, ok := results["call-alpha"]
	if !ok {
		t.Fatal("alpha's delegate result is missing")
	}
	beta, ok := results["call-beta"]
	if !ok {
		t.Fatal("beta's delegate result is missing")
	}

	// Each body is exactly its own child's final answer.
	if !strings.Contains(alpha.Content, "alpha marker report A11 done.") {
		t.Fatalf("alpha report lost its own body:\n%.300q", alpha.Content)
	}
	if strings.Contains(alpha.Content, "beta") {
		t.Fatalf("alpha report swallowed sibling material:\n%.300q", alpha.Content)
	}
	if !strings.Contains(beta.Content, "beta burst three. B33") {
		t.Fatalf("beta report lost its final turn:\n%.300q", beta.Content)
	}
	if strings.Contains(beta.Content, "alpha") {
		t.Fatalf("beta report swallowed sibling material:\n%.300q", beta.Content)
	}
	// Each header counts its own turns: honest per-child accounting.
	if !strings.Contains(alpha.Content, "turns=1") || !strings.Contains(beta.Content, "turns=3 tool_calls=2") {
		t.Fatalf("turn accounting not per-child (alpha %.120q / beta %.120q)", alpha.Content, beta.Content)
	}

	// Progress lines carry the agent name and count one line per turn,
	// in order, per agent - interleaving with another child must not
	// commit a shared-counter mixup.
	type agentLines struct {
		name  string
		turns []int
	}
	lines := map[string]*agentLines{"alpha": {name: "alpha"}, "beta": {name: "beta"}}
	for _, ev := range drained {
		if ev.Type != EventToolProgress || ev.ToolProgress == nil ||
			ev.ToolProgress.Stream != "progress" || !strings.HasPrefix(ev.ToolProgress.Data, "[delegate: agent") {
			continue
		}
		var name string
		var turn int
		if n, err := fmt.Sscanf(ev.ToolProgress.Data, "[delegate: agent %q turn %d]", &name, &turn); err != nil || n != 2 {
			continue // other tools may stream [delegate: ...] shaped lines
		}
		a, ok := lines[name]
		if !ok {
			t.Fatalf("progress line for unknown agent %q", name)
		}
		a.turns = append(a.turns, turn)
	}
	if got := lines["alpha"].turns; len(got) != 1 || got[0] != 1 {
		t.Fatalf("alpha progress turns = %v, want [1]", got)
	}
	if got := lines["beta"].turns; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("beta progress turns = %v, want [1 2 3] in arrival order", got)
	}
}

// ---------------------------------------------------------------------------
// Three-level cancellation with a parked interactive ask (ORCH-TEST-03).
// ---------------------------------------------------------------------------

// gatedOnceProvider scripts turns like keyedProvider but parks exactly
// one child's first inference on a test-controlled gate, so the scenario
// below is established by that park (and by channel/detector waits), not
// by sleeps. Everything else matches keyedProvider: keyed child turns
// keyed by the delegated task text, instant buffered delivery, and
// fail-fast context.Canceled for out-of-script calls.
type gatedOnceProvider struct {
	mu          sync.Mutex
	parentTurns [][]providers.StreamEvent
	parentIdx   int
	childTurns  map[string][][]providers.StreamEvent
	childCalls  map[string]int

	// parkKey is the one task key whose FIRST StreamChat call parks on
	// gate. A second call for that key runs out of script and fails
	// fast, so the parked leg can never extend its own budget use.
	parkKey string
	gate    chan struct{}

	// arrivalSignal carries one token when the parked call enters: the
	// empty-thinking turn marker that charges the aggregate budget has
	// already been emitted by runModelTurn before the provider is called.
	arrivalSignal chan struct{}
}

func newGatedOnceProvider(parentTurns [][]providers.StreamEvent, childTurns map[string][][]providers.StreamEvent, parkKey string) *gatedOnceProvider {
	return &gatedOnceProvider{
		parentTurns:   parentTurns,
		childTurns:    childTurns,
		childCalls:    map[string]int{},
		parkKey:       parkKey,
		gate:          make(chan struct{}),
		arrivalSignal: make(chan struct{}, 32),
	}
}

func (p *gatedOnceProvider) StreamChat(ctx context.Context, messages []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	var turn []providers.StreamEvent
	var gate chan struct{}

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
		idx := p.childCalls[key]
		if !ok || idx >= len(turns) {
			p.mu.Unlock()
			return nil, context.Canceled // out of script for this child
		}
		turn = turns[idx]
		if key == p.parkKey && idx == 0 {
			gate = p.gate
			select {
			case p.arrivalSignal <- struct{}{}:
			default:
			}
		}
		p.childCalls[key]++
	} else {
		// Parent turn: strictly sequential script. An extra root
		// inference (a post-cancellation turn) must fail fast.
		if p.parentIdx >= len(p.parentTurns) {
			p.mu.Unlock()
			return nil, context.Canceled // out of parent script
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

	// Park: return immediately and deliver the scripted turn only once
	// release() opens the gate. Cancellation exits with an empty stream
	// so a cancelled run cannot wedge forever (mirrors
	// gatedChildrenProvider's ctx exit).
	events := make(chan providers.StreamEvent, len(turn))
	go func() {
		select {
		case <-gate:
		case <-ctx.Done():
			close(events)
			return
		}
		for _, event := range turn {
			events <- event
		}
		close(events)
	}()
	return events, nil
}

// release opens the parked child's gate once.
func (p *gatedOnceProvider) release() {
	p.mu.Lock()
	g := p.gate
	p.mu.Unlock()
	close(g)
}

// parentCalls counts consumed root script entries.
func (p *gatedOnceProvider) parentCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.parentIdx
}

// childCallCount reports how many provider calls one keyed child made.
func (p *gatedOnceProvider) childCallCount(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.childCalls[key]
}

// cancelGateSink records every orchestration event and, when the budget
// crossing fires, signals the test without ever blocking the run.
type cancelGateSink struct {
	mu        sync.Mutex
	events    []orchevents.Event
	exhausted chan struct{}
}

func (s *cancelGateSink) Emit(e orchevents.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	if e.Type == orchevents.BudgetExhausted {
		select {
		case s.exhausted <- struct{}{}:
		default:
		}
	}
}

func (s *cancelGateSink) ofType(t orchevents.Type) []orchevents.Event {
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

// newThreeLevelAdversarialRuntime wires the production permission shape
// (rules manager, operator asker, shared ask scope) exactly like
// newPermissionAdversarialRuntime, but registers the three-agent tree the
// scenario needs: mid delegates leaf (depth 2), leaf delegates again
// (depth 3), and uncle carries echo so its permission ask holds the
// shared ask scope when cancellation arrives.
func newThreeLevelAdversarialRuntime(t *testing.T, p providers.ModelProvider, asker permissions.Asker) (*Runtime, *countingEchoTool) {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	echo := &countingEchoTool{}
	if err := full.Register(echo); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	registry := agent.NewRegistry()
	for _, def := range []agent.Definition{
		{Name: "mid", Description: "mid-level child agent", SystemPrompt: "child-system", Tools: []string{"delegate"}, Skills: []string{}},
		{Name: "leaf", Description: "deepest child agent", SystemPrompt: "child-system", Tools: []string{"delegate"}, Skills: []string{}},
		{Name: "uncle", Description: "sibling child agent", SystemPrompt: "child-system", Tools: []string{"echo"}, Skills: []string{}},
	} {
		if err := registry.Register(def); err != nil {
			t.Fatalf("register agent %s: %v", def.Name, err)
		}
	}
	orch := newOrchestrator(config.Orchestration{
		Enabled:               true,
		MaxDepth:              intPtr(3),
		MaxConcurrentChildren: intPtr(4),
		MaxTotalChildTurns:    intPtr(4),
		ChildPermissions:      config.ChildPermissionsInteractive,
	}, registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
	}
	manager := permissions.NewManagerWithRules(delegateAllowRules(), newRecordingRulesStore(delegateAllowRules()))
	r := &Runtime{
		provider:    p,
		agent:       agent.New("root", "root-system", ""),
		activeAgent: "root",
		manager:     full,
		fullManager: full,
		orch:        orch,
		scheduler:   newScheduler(full, manager, asker, DefaultSchedulerConfig),
	}
	orch.parent = r
	r.scheduler.askSerial = orch.childAskSerial()
	return r, echo
}

// TestOrchestrationThreeLevelCancelUnblocksInteractiveAsk pins the
// ORCH-TEST-03 regression: at max_depth=3 a sibling parked on the shared
// interactive ask scope must unblock cleanly when cancellation tears down
// the tree, while the grandchild leg legitimately exhausts
// max_total_child_turns at the crossing.
//
// Script (ordering established by gates and channels, never sleeps):
//  1. Root turn 1 delegates mid and uncle concurrently. Pre-release the
//     shared pool holds exactly three turn-markers: mid#1, uncle#1, and
//     leaf#1 (the leaf leg parks in the provider after its marker).
//  2. uncle's echo ask enters the shared ask scope and parks there.
//  3. Releasing the leaf delivers its delegate call: the depth-3 text
//     child takes the 4th marker (still in budget), joins, and leaf's
//     own second inference takes the 5th - over the limit - so the
//     orchestrator cancels leaf and fires BudgetExhausted exactly once.
//  4. The test cancels the root while uncle is parked inside the ask
//     scope: the ask unwinds via its context, every join completes, and
//     no goroutine leaks.
func TestOrchestrationThreeLevelCancelUnblocksInteractiveAsk(t *testing.T) {
	provider := newGatedOnceProvider(
		[][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{
					delegateCall("call-mid", "mid", "mid: nested leg"),
					delegateCall("call-uncle", "uncle", "uncle: echo leg"),
				}},
				{Done: true},
			},
		},
		map[string][][]providers.StreamEvent{
			"mid: nested leg": {
				{
					{ToolCalls: []providers.ToolCall{
						delegateCall("call-leaf", "leaf", "leaf: exhaust leg"),
					}},
					{Done: true},
				},
			},
			// Parked first inference: delivered only on release().
			"leaf: exhaust leg": {
				{
					{ToolCalls: []providers.ToolCall{
						delegateCall("call-leaf3", "leaf", "leaf3: text leg"),
					}},
					{Done: true},
				},
			},
			"leaf3: text leg": oneTextTurn("leaf3 answer."),
			"uncle: echo leg": {
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-echo",
						Name:      "echo",
						Arguments: map[string]any{"value": "ping"},
					}}},
					{Done: true},
				},
			},
		},
		"leaf: exhaust leg")

	asker := newGatedAsker(permissions.PromptAllowOnce)
	rt, echo := newThreeLevelAdversarialRuntime(t, provider, asker)
	sink := &cancelGateSink{exhausted: make(chan struct{}, 1)}
	rt.orch.setSink(sink)

	base := countStableGoroutines(t)

	ctx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	events, err := rt.StreamChat(ctx, []providers.Message{{
		Role: providers.UserRole, Content: "coordinate three levels",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	drainDone := make(chan struct{})
	var runEvents []Event
	go func() {
		defer close(drainDone)
		for e := range events {
			runEvents = append(runEvents, e)
		}
	}()

	// Stage 1: the leaf leg's first inference has parked (its turn
	// marker is already charged into the shared pool).
	select {
	case <-provider.arrivalSignal:
	case <-time.After(10 * time.Second):
		t.Fatalf("deadlock detector: leaf leg never parked in the provider")
	}
	// Stage 2: uncle's echo ask has entered the shared ask scope. This
	// also proves uncle#1's marker landed, pinning the pre-release pool
	// at exactly three charges.
	select {
	case <-asker.entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("deadlock detector: no interactive ask was entered")
	}
	if got := asker.askCount(); got != 1 {
		t.Fatalf("asker entries = %d, want exactly one (uncle's echo)", got)
	}
	req := asker.request(0)
	if req.Tool != "echo" {
		t.Fatalf("asked tool = %q, want echo", req.Tool)
	}
	if req.Origin == nil || req.Origin.Agent != "uncle" || req.Origin.Chain != "root > uncle" {
		t.Fatalf("ask origin = %+v, want agent %q chain %q", req.Origin, "uncle", "root > uncle")
	}

	// Release the leaf: the depth-3 text leg joins within budget, then
	// leaf's second inference crosses the aggregate limit, the
	// orchestrator stops leaf, and the exhaustion reports exactly once.
	provider.release()
	select {
	case <-sink.exhausted:
	case <-time.After(10 * time.Second):
		t.Fatalf("deadlock detector: BudgetExhausted never fired (%d child progress events recorded)",
			len(sink.ofType(orchevents.ChildProgress)))
	}

	// Cancel the root while uncle is parked inside the shared ask scope;
	// the whole tree must join and the stream must close.
	cancelRoot()
	select {
	case <-drainDone:
	case <-time.After(10 * time.Second):
		t.Fatalf("deadlock detector: run stream did not close after cancellation")
	}
	waitForStableGoroutines(t, base)

	// The exhaustion is one event, honestly attributed to the crossing
	// child at depth 2 on the mid leg.
	budget := sink.ofType(orchevents.BudgetExhausted)
	if len(budget) != 1 {
		t.Fatalf("BudgetExhausted events = %d, want exactly 1", len(budget))
	}
	b := budget[0]
	if b.Budget != orchevents.BudgetTotalTurns {
		t.Errorf("budget name = %q, want %q", b.Budget, orchevents.BudgetTotalTurns)
	}
	if b.BudgetLimit != 4 || b.BudgetValue != 5 {
		t.Errorf("budget limit/value = %d/%d, want 4/5 (crossed at the 5th charge)", b.BudgetLimit, b.BudgetValue)
	}
	if b.Agent != "leaf" || b.Chain != "root > mid > leaf" || b.Depth != 2 {
		t.Errorf("exhaustion attribution = agent %q chain %q depth %d, want leaf root > mid > leaf depth 2", b.Agent, b.Chain, b.Depth)
	}
	if b.ChildRunID == "" || b.Reason == "" {
		t.Errorf("exhaustion must carry a child run id and a reason (id=%q reason=%q)", b.ChildRunID, b.Reason)
	}

	// All four delegations were requested, admitted, and spawned; none
	// were refused at depth, spawn, task, or admission budget.
	for _, want := range []struct {
		typ  orchevents.Type
		want int
	}{
		{orchevents.DelegationRequested, 4},
		{orchevents.DelegationAccepted, 4},
		{orchevents.DelegationRejected, 0},
		{orchevents.AgentSpawned, 4},
	} {
		if got := len(sink.ofType(want.typ)); got != want.want {
			t.Errorf("%s events = %d, want %d", want.typ, got, want.want)
		}
	}

	// Terminal child states: the depth-3 text leg completes; mid, uncle,
	// and leaf all end cancelled (budget stop on mid and leaf, root
	// cancellation on uncle). No path may end in error.
	completed := sink.ofType(orchevents.ChildCompleted)
	if len(completed) != 4 {
		t.Fatalf("ChildCompleted events = %d, want 4", len(completed))
	}
	cancelChains := map[string]int{}
	doneChains := 0
	for _, e := range completed {
		switch e.Outcome {
		case orchevents.OutcomeDone:
			doneChains++
			if e.Chain != "root > mid > leaf > leaf" {
				t.Errorf("done child chain = %q, want the depth-3 text leg", e.Chain)
			}
		case orchevents.OutcomeCancelled:
			cancelChains[e.Chain]++
		default:
			t.Errorf("child at %q ended with outcome %q (%s), want done or cancelled", e.Chain, e.Outcome, e.Error)
		}
	}
	if doneChains != 1 {
		t.Errorf("done children = %d, want 1", doneChains)
	}
	wantCancelled := map[string]int{"root > mid": 1, "root > uncle": 1, "root > mid > leaf": 1}
	for chain, want := range wantCancelled {
		if cancelChains[chain] != want {
			t.Errorf("cancelled children for %q = %d, want %d (all: %v)", chain, cancelChains[chain], want, cancelChains)
		}
	}
	for _, e := range completed {
		if e.Outcome == orchevents.OutcomeCancelled && wantCancelled[e.Chain] == 0 {
			t.Errorf("unexpected cancelled child chain %q", e.Chain)
		}
	}

	// Root stream: the run must end by cancellation only.
	// EventCancelled itself may be dropped by the non-blocking emit
	// path, but Done and Error would mean the cancelled tree still
	// reported a normal completion.
	for _, e := range runEvents {
		if e.Type == EventDone || e.Type == EventError {
			t.Fatalf("root stream carried %v; cancellation must be the only terminal", e.Type)
		}
	}
	// Uncle's echo never executed: its ask was cancelled, not approved.
	if got := echo.count(); got != 0 {
		t.Errorf("echo executed %d times, want 0 (ask cancelled before approval)", got)
	}
	// The root took exactly one inference: no turn started past the
	// cancellation.
	if got := provider.parentCalls(); got != 1 {
		t.Errorf("root provider calls = %d, want exactly 1", got)
	}
	// The depth-3 text leg and uncle each ran their single scripted turn.
	if got := provider.childCallCount("leaf3: text leg"); got != 1 {
		t.Errorf("leaf3 provider calls = %d, want exactly 1 (single clean text turn)", got)
	}
	if got := provider.childCallCount("uncle: echo leg"); got != 1 {
		t.Errorf("uncle provider calls = %d, want exactly 1", got)
	}
	// The mid leg's delegate terminal: cancellation may land while the
	// joined tree is still unwinding, and the scheduler then reports the
	// cancellation instead of surfacing the internal child report
	// (shipped semantics: a cancelled run's late-won results never leak
	// back). Whether root cancellation beats that unwind is timing, so
	// the observation stays optional, but if the terminal is seen it
	// must be an error - the scheduler's cancellation report or the
	// honest budget report - never a success.
	var midReport *ToolResult
	for _, e := range runEvents {
		if e.ToolResult != nil && e.ToolResult.ToolCallID == "call-mid" &&
			(e.Type == EventToolFinish || e.Type == EventToolFailed ||
				e.Type == EventToolDenied || e.Type == EventToolCancelled) {
			midReport = e.ToolResult
		}
	}
	if midReport != nil {
		if !midReport.IsError {
			t.Errorf("mid delegate terminal reported success (%.160q); a cancelled delegation must never succeed", midReport.Content)
		}
		if !strings.Contains(midReport.Content, "tool execution cancelled") &&
			!strings.Contains(midReport.Content, "aggregate child-turn budget exhausted") {
			t.Errorf("mid delegate terminal = %.200q; want the cancellation report or the budget report", midReport.Content)
		}
	}
}
