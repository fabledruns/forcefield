package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// gateOverlapShared carries the instrumentation behind a
// gateOverlapProvider family: one process-wide view of what the parent
// run and every run-scoped child saw, so the test can tell a clone
// family apart from a single shared instance without exotic
// reflection. All fields are mu-guarded.
type gateOverlapShared struct {
	mu         sync.Mutex
	inflight   int
	peak       int
	conflicts  int
	total      int
	nextID     int
	usage      map[int]map[string]bool // instance id -> call identities served
	reasoning  map[int]int             // instance id -> SetReasoning calls
	barrierIdents map[string]bool      // identities that must genuinely overlap
	barrierMu            sync.Mutex
	barrierArrived       int
	barrierOpened        bool
	barrierDone          chan struct{}
}

func newGateOverlapShared(barrierIdents ...string) *gateOverlapShared {
	set := map[string]bool{}
	for _, id := range barrierIdents {
		set[id] = true
	}
	return &gateOverlapShared{
		usage:         map[int]map[string]bool{},
		reasoning:     map[int]int{},
		barrierIdents: set,
	}
}

// registerInstance hands out a fresh instance identity. The root
// provider registers once at construction; every CloneForRun registers
// at clone time, so the test learns the exact instance topology.
func (s *gateOverlapShared) registerInstance() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := s.nextID
	s.usage[id] = map[string]bool{}
	return id
}

func (s *gateOverlapShared) note(identity string, id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total++
	s.usage[id][identity] = true
}

func (s *gateOverlapShared) noteReasoning(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reasoning[id]++
}

// enter/leave bracket one in-flight provider call, feeding the peak.
func (s *gateOverlapShared) enter() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight++
	if s.inflight > s.peak {
		s.peak = s.inflight
	}
}

func (s *gateOverlapShared) leave() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight--
}

func (s *gateOverlapShared) conflict() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conflicts++
}

// barrierWait lets listed call identities genuinely overlap: the first
// call holds its slot until the expected number arrived (or the fail-open
// timeout or cancellation hits, so a changed sibling topology never
// wedges the run), then all of them stream together. Participants hold
// their own instance gate while waiting, so a broken sharing topology
// manifests as recorded conflicts at the second participant instead of
// silent serialization.
func (s *gateOverlapShared) barrierWait(identity string, ctx context.Context, timeout time.Duration) error {
	s.barrierMu.Lock()
	if !s.barrierIdents[identity] {
		s.barrierMu.Unlock()
		return nil
	}
	if s.barrierDone == nil {
		s.barrierDone = make(chan struct{})
	}
	done := s.barrierDone
	s.barrierArrived++
	if s.barrierArrived >= len(s.barrierIdents) && !s.barrierOpened {
		close(done)
		s.barrierOpened = true
	}
	s.barrierMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout):
		// Fail-open: assertions below catch the wrong shape; never wedge.
		return nil
	}
}

func (s *gateOverlapShared) barrierArrivals() int {
	s.barrierMu.Lock()
	defer s.barrierMu.Unlock()
	return s.barrierArrived
}

func (s *gateOverlapShared) stats() (inflight, peak, conflicts, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight, s.peak, s.conflicts, s.total
}

func (s *gateOverlapShared) instances() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int, 0, len(s.usage))
	for id := range s.usage {
		ids = append(ids, id)
	}
	return ids
}

// servedBy reports which instances served a call identity. A healthy
// family serves each identity exactly once, on the instance owned by
// that run.
func (s *gateOverlapShared) servedBy(identity string) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []int
	for id, seen := range s.usage {
		if seen[identity] {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *gateOverlapShared) identitySet(id int) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]bool(s.usage[id])
}

func (s *gateOverlapShared) reasoningCalls(id int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reasoning[id]
}

// gateOverlapProvider is a single-flight fake provider family for the
// run-scoped cloning regression: it reimplements the built-in adapters'
// gate semantics (cap-1 per instance; overlaps surface as
// providers.ErrInFlight, which the retry loop would then consume) and
// supports run-scoped cloning with fresh per-instance state and a
// shared-safe pointer to the family stats. Parent turns are consumed in
// call order; child turns are keyed by the delegated task text. Each
// call holds its slot for streamMS so genuine overlap is observable.
// Out-of-script calls fail fast so a changed route never stalls the
// run's deadline silently.
type gateOverlapProvider struct {
	shared *gateOverlapShared
	self   int

	streamMS time.Duration

	parentTurns [][]providers.StreamEvent
	childTurns  map[string][][]providers.StreamEvent

	// route holds this instance's own script position. It is a pointer
	// so a clone must allocate fresh state instead of copying (and the
	// mutex inside stays behind the pointer, per vet's lock-copy rule).
	route *gateOverlapRouteState

	gate      chan struct{}
	reasoning providers.ReasoningConfig
}

// gateOverlapRouteState tracks which scripted turn this instance serves
// next. Siblings never share it: each run-scoped clone gets its own.
type gateOverlapRouteState struct {
	mu         sync.Mutex
	parentIdx  int
	childCalls map[string]int
}

func newGateOverlapProvider(streamMS time.Duration, parentTurns [][]providers.StreamEvent, childTurns map[string][][]providers.StreamEvent, barrierIdents ...string) *gateOverlapProvider {
	p := &gateOverlapProvider{
		shared:      newGateOverlapShared(barrierIdents...),
		streamMS:    streamMS,
		parentTurns: parentTurns,
		childTurns:  childTurns,
		route: &gateOverlapRouteState{
			childCalls: map[string]int{},
		},
		gate: make(chan struct{}, 1),
	}
	p.self = p.shared.registerInstance()
	return p
}

// CloneForRun is the run-scoped capability the runtime exercises: the
// clone keeps the family stats pointer (shared-safe) and the scripted
// route table (construction-time, read-only), and takes fresh per-run
// state: own gate, own routing counters, zero reasoning.
func (p *gateOverlapProvider) CloneForRun() (providers.ModelProvider, error) {
	c := *p
	c.self = p.shared.registerInstance()
	c.gate = make(chan struct{}, 1)
	c.reasoning = providers.ReasoningConfig{}
	c.route = &gateOverlapRouteState{
		childCalls: map[string]int{},
	}
	return &c, nil
}

// SetReasoning/GetReasoning record the receiver identity, so the test
// can prove the concurrent runs configured independent reasoning state
// even though every run calls SetReasoning per model turn.
func (p *gateOverlapProvider) SetReasoning(cfg providers.ReasoningConfig) {
	p.shared.noteReasoning(p.self)
	p.reasoning = cfg
}

func (p *gateOverlapProvider) GetReasoning() providers.ReasoningConfig {
	return p.reasoning
}

func lastUserTask(messages []providers.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == providers.UserRole {
			return messages[i].Content
		}
	}
	return ""
}

func (p *gateOverlapProvider) StreamChat(ctx context.Context, messages []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	var identity, key string
	var callNo int
	var turn []providers.StreamEvent

	p.route.mu.Lock()
	if len(messages) > 0 && strings.Contains(messages[0].Content, "child-system") {
		key = lastUserTask(messages)
		if p.route.childCalls == nil {
			p.route.childCalls = map[string]int{}
		}
		p.route.childCalls[key]++
		callNo = p.route.childCalls[key]
		identity = key + "#" + strconv.Itoa(callNo)
		turns, ok := p.childTurns[key]
		if !ok || callNo > len(turns) {
			p.route.mu.Unlock()
			return nil, context.Canceled // out of script for this child
		}
		turn = turns[callNo-1]
	} else {
		if p.route.parentIdx >= len(p.parentTurns) {
			p.route.mu.Unlock()
			return nil, context.Canceled // out of parent script
		}
		p.route.parentIdx++
		identity = fmt.Sprintf("parent#%d", p.route.parentIdx)
		turn = p.parentTurns[p.route.parentIdx-1]
	}
	p.route.mu.Unlock()

	p.shared.note(identity, p.self)

	// Cap-1 single-flight: exactly the built-in adapters' contract. An
	// overlap on one instance is a genuine error (the retry loop would
	// consume it silently, so the family records it for the test).
	select {
	case p.gate <- struct{}{}:
	default:
		p.shared.conflict()
		return nil, providers.ErrInFlight
	}
	defer func() {
		select {
		case <-p.gate:
		default:
		}
	}()

	p.shared.enter()
	defer p.shared.leave()

	// Listed identities must genuinely overlap, not merely both run.
	if err := p.shared.barrierWait(identity, ctx, 3*time.Second); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(p.streamMS):
	}

	events := make(chan providers.StreamEvent, len(turn))
	for _, event := range turn {
		events <- event
	}
	close(events)
	return events, nil
}

// TestDelegateChildrenCloneRunScoped pins the concurrency regression for
// run-scoped cloning: the parent's provider is cloned per child run, so
// two concurrent children stream through two distinct single-flight
// gates (zero ErrInFlight conflicts, peak in-flight >= 2), and no shared
// mutable provider apples-to-apples serves two runs.
func TestDelegateChildrenCloneRunScoped(t *testing.T) {
	provider := newGateOverlapProvider(
		300*time.Millisecond,
		[][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{
					{ID: "call-delegate-a", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "a"}},
					{ID: "call-delegate-b", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "b"}},
				}},
				{Done: true},
			},
			{{Text: "parent done."}, {Done: true}},
		},
		map[string][][]providers.StreamEvent{
			"a": {{{Text: "childA done"}, {Done: true}}},
			"b": {{{Text: "childB done"}, {Done: true}}},
		},
		"a#1",
		"b#1",
	)
	runtime := newProbeRuntime(t, provider, childConfig(t, 2, 16), newChildRegistryForTests())

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
	joined := strings.Join(bodies, "\n---\n")
	if !strings.Contains(joined, "childA done") || !strings.Contains(joined, "childB done") {
		t.Fatalf("delegate bodies = %q, want both children's verdicts", joined)
	}

	inflight, peak, conflicts, total := provider.shared.stats()
	if inflight != 0 {
		t.Fatalf("in-flight = %d at drain, want every call released its slot", inflight)
	}
	if conflicts != 0 {
		t.Fatalf("gate conflicts = %d, want 0: sibling child runs must not share one single-flight instance", conflicts)
	}
	if peak < 2 {
		t.Fatalf("peak in-flight calls = %d, want 2 (children must genuinely overlap)", peak)
	}
	if total != 4 {
		t.Fatalf("provider calls = %d, want 4 (2 parent turns + 2 child turns)", total)
	}

	rootID := provider.self
	if rootID != 1 {
		t.Fatalf("root instance id = %d, want 1 (registered before any clone)", rootID)
	}
	for id, served := range provider.shared.identitySet(rootID) {
		if !served {
			continue
		}
		if !strings.HasPrefix(id, "parent#") {
			t.Errorf("root instance served child call %q; children must use their own clones", id)
		}
	}
	instA := provider.shared.servedBy("a#1")
	instB := provider.shared.servedBy("b#1")
	if len(instA) != 1 || len(instB) != 1 {
		t.Fatalf("call identity instances: a#1=%v b#1=%v, want exactly one instance each", instA, instB)
	}
	if instA[0] == rootID || instB[0] == rootID {
		t.Fatalf("child calls served by the root instance (a#1->%d b#1->%d root=%d)", instA[0], instB[0], rootID)
	}
	if instA[0] == instB[0] {
		t.Fatalf("sibling children share one instance %d; each run must clone its own", instA[0])
	}
	if ids := provider.shared.instances(); len(ids) != 3 {
		t.Errorf("distinct instances = %d (%v), want 3 (root + two child clones)", len(ids), ids)
	}
	for _, id := range []int{rootID, instA[0], instB[0]} {
		if got := provider.shared.reasoningCalls(id); got < 1 {
			t.Errorf("instance %d saw %d SetReasoning calls, want at least one per run", id, got)
		}
	}
}

// TestDelegateGrandchildClonesFromParentClone pins the nesting leg of
// run-scoped cloning: a grandchild clones from its immediate parent's
// cloned snapshot, so every run on the chain (root > child > grandchild)
// owns a distinct provider instance while the child's own two turns
// stream through one single clone.
func TestDelegateGrandchildClonesFromParentClone(t *testing.T) {
	provider := newGateOverlapProvider(
		120*time.Millisecond,
		[][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{
					{ID: "call-delegate-lead", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "lead"}},
				}},
				{Done: true},
			},
			{{Text: "parent done."}, {Done: true}},
		},
		map[string][][]providers.StreamEvent{
			"lead": {
				{
					{ToolCalls: []providers.ToolCall{
						{ID: "call-delegate-nested", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "nested"}},
					}},
					{Done: true},
				},
				{{Text: "lead done"}, {Done: true}},
			},
			"nested": {{{Text: "nested done"}, {Done: true}}},
		},
	)
	cfg := childConfig(t, 2, 16)
	cfg.MaxDepth = intPtr(2)
	runtime := newProbeRuntime(t, provider, cfg, newChildRegistryForTests())

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "nest"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var report string
	for e := range events {
		if e.Type == EventToolFinish && e.ToolResult != nil && e.ToolResult.Name == "delegate" {
			report = e.ToolResult.Content
		}
	}
	if !strings.Contains(report, "lead done") {
		t.Fatalf("delegate report = %q, want the child's verdict with the nested run inside", report)
	}

	inflight, _, conflicts, total := provider.shared.stats()
	if inflight != 0 {
		t.Fatalf("in-flight = %d at drain, want every call released its slot", inflight)
	}
	if conflicts != 0 {
		t.Fatalf("gate conflicts = %d, want 0 even on the nested chain", conflicts)
	}
	if total != 5 {
		t.Fatalf("provider calls = %d, want 5 (p, lead, nested, lead-final, p-final)", total)
	}

	rootID := provider.self
	lead := provider.shared.servedBy("lead#1")
	nested := provider.shared.servedBy("nested#1")
	if len(lead) != 1 || len(nested) != 1 {
		t.Fatalf("call identity instances: lead#1=%v nested#1=%v, want exactly one instance each", lead, nested)
	}
	if lead[0] == rootID || nested[0] == rootID {
		t.Fatalf("nested chain served by the root instance (lead->%d nested->%d root=%d)", lead[0], nested[0], rootID)
	}
	if lead[0] == nested[0] {
		t.Fatalf("grandchild shares its parent's instance %d; the grandchild must clone from the parent's clone", lead[0])
	}
	// The child's own two turns stream through ONE run-scoped clone.
	if !provider.shared.identitySet(lead[0])["lead#2"] {
		t.Errorf("lead#2 served elsewhere than instance %d; a run must keep one provider instance for all its turns", lead[0])
	}
	if ids := provider.shared.instances(); len(ids) != 3 {
		t.Errorf("distinct instances = %d (%v), want 3 (root + child clone + grandchild clone)", len(ids), ids)
	}
	for _, id := range []int{rootID, lead[0], nested[0]} {
		if got := provider.shared.reasoningCalls(id); got < 1 {
			t.Errorf("instance %d saw %d SetReasoning calls, want at least one per run", id, got)
		}
	}
}

// TestDelegateCancelReleasesClonedGates pins that cancelling the parent
// while two cloned children are mid-stream drains every clone's slot:
// peak in-flight shows both children genuinely streaming, and after the
// drain every gate is free again (no leaked slot on a clone instance).
func TestDelegateCancelReleasesClonedGates(t *testing.T) {
	provider := newGateOverlapProvider(
		600*time.Millisecond,
		[][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{
					{ID: "call-delegate-a", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "a"}},
					{ID: "call-delegate-b", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "b"}},
				}},
				{Done: true},
			},
		},
		map[string][][]providers.StreamEvent{
			"a": {{{Text: "childA text"}, {Done: true}}},
			"b": {{{Text: "childB text"}, {Done: true}}},
		},
		"a#1",
		"b#1",
	)
	runtime := newProbeRuntime(t, provider, childConfig(t, 2, 16), newChildRegistryForTests())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel exactly once both child calls genuinely stream (both
	// barrier identities arrived), so the drain is exercised from a
	// realistic two-clone overlap rather than a pre-start race.
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.After(5 * time.Second)
		for {
			if provider.shared.barrierArrivals() >= 2 {
				cancel()
				return
			}
			select {
			case <-deadline:
				cancel()
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()

	sawCancelled := false
	events, err := runtime.StreamChat(ctx, []providers.Message{
		{Role: providers.UserRole, Content: "cancel us"},
	})
	if err != nil {
		cancel()
		t.Fatalf("StreamChat() error = %v", err)
	}
	for e := range events {
		if e.Type == EventCancelled {
			sawCancelled = true
		}
	}
	if err := ctx.Err(); err == nil {
		cancel()
	}
	<-done

	if !sawCancelled {
		t.Error("run never reported EventCancelled after the provider-level cancellation")
	}
	inflight, peak, conflicts, _ := provider.shared.stats()
	if inflight != 0 {
		t.Errorf("in-flight = %d after cancellation drained the run, want every clone's slot released", inflight)
	}
	if conflicts != 0 {
		t.Errorf("gate conflicts = %d, want 0: cancellation must not route one run's traffic into another run's clone", conflicts)
	}
	if peak < 2 {
		t.Errorf("peak in-flight = %d, want 2: both children must have streamed concurrently before the cancel", peak)
	}
	for _, id := range []string{"a#1", "b#1"} {
		if got := provider.shared.servedBy(id); len(got) == 0 {
			t.Errorf("identity %q never reached a provider instance; the cancel test never exercised the join", id)
		}
	}
}
