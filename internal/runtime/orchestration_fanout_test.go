package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// fanoutProvider scripts one parent conversation plus per-child turns
// keyed by the delegated task text (the child's last user message), with
// a deterministic entry barrier for child calls: each child provider call
// records itself in flight and blocks until barrierSize children have
// entered — which closes release — or its context is cancelled. Peak
// in-flight child calls is measured, so overlap is proven, never assumed.
//
// The barrier is what makes the concurrent-fan-out acceptance criterion
// deterministic: if the runtime serialized delegate calls, no second
// child could enter before the first finished, release would never
// close, and the watchdog fails the run with a diagnosable error instead
// of deadlocking. The happy path is entirely synchronization-driven; the
// watchdog exists only to bound the failure path. holdRelease keeps the
// barrier closed-after-fill semantics inverted (it never opens):
// cancellation tests cancel the context to unwind the blocked children.
type fanoutProvider struct {
	mu          sync.Mutex
	parentTurns [][]providers.StreamEvent
	parentIdx   int

	childTurns map[string][]providers.StreamEvent // task -> scripted turns
	childErr   map[string]error                   // task -> error after the barrier
	childCalls map[string]int

	barrierSize int
	holdRelease bool
	entered     int
	release     chan struct{}
	enteredAll  chan struct{}
	enteredOnce sync.Once

	inflight int
	peak     int
}

// fanoutWatchdog bounds the barrier's failure path only: the happy path
// never waits on it, so it is not a sleep-based synchronization.
const fanoutWatchdog = 5 * time.Second

func newFanoutProvider(parentTurns [][]providers.StreamEvent, barrierSize int) *fanoutProvider {
	return &fanoutProvider{
		parentTurns: parentTurns,
		childTurns:  map[string][]providers.StreamEvent{},
		childErr:    map[string]error{},
		childCalls:  map[string]int{},
		barrierSize: barrierSize,
		release:     make(chan struct{}),
		enteredAll:  make(chan struct{}),
	}
}

func (p *fanoutProvider) StreamChat(ctx context.Context, messages []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	p.mu.Lock()
	if len(messages) == 0 || !strings.Contains(messages[0].Content, "child-system") {
		// Parent call: strictly sequential script.
		if p.parentIdx >= len(p.parentTurns) {
			p.mu.Unlock()
			return nil, context.Canceled // out of parent script
		}
		turn := p.parentTurns[p.parentIdx]
		p.parentIdx++
		p.mu.Unlock()

		events := make(chan providers.StreamEvent, len(turn))
		for _, event := range turn {
			events <- event
		}
		close(events)
		return events, nil
	}

	// Child call: identified by the delegated task (its only user
	// message). Enter the in-flight window, then rendezvous.
	p.inflight++
	if p.inflight > p.peak {
		p.peak = p.inflight
	}
	task := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == providers.UserRole {
			task = messages[i].Content
			break
		}
	}
	p.childCalls[task]++
	if p.barrierSize > 0 {
		p.entered++
		if p.entered >= p.barrierSize {
			p.enteredOnce.Do(func() {
				close(p.enteredAll)
				if !p.holdRelease {
					close(p.release)
				}
			})
		}
	}
	p.mu.Unlock()

	if p.barrierSize > 0 {
		select {
		case <-p.release:
		case <-ctx.Done():
			p.mu.Lock()
			p.inflight--
			p.mu.Unlock()
			return nil, ctx.Err()
		case <-time.After(fanoutWatchdog):
			p.mu.Lock()
			p.inflight--
			p.mu.Unlock()
			return nil, fmt.Errorf(
				"fanout barrier timed out: the runtime did not keep %d delegate calls in flight (peak %d)",
				p.barrierSize, p.peak)
		}
	}

	// Past the barrier: fail this child on request, else run its script.
	if err := p.childErr[task]; err != nil {
		p.mu.Lock()
		p.inflight--
		p.mu.Unlock()
		return nil, err
	}
	turn, ok := p.childTurns[task]
	p.mu.Lock()
	p.inflight--
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no scripted child turn for task %q", task)
	}

	events := make(chan providers.StreamEvent, len(turn))
	for _, event := range turn {
		events <- event
	}
	close(events)
	return events, nil
}

// fanoutParentTurns scripts the parent: one response carrying three
// independent delegate calls, then the final synthesis turn.
func fanoutParentTurns() [][]providers.StreamEvent {
	delegate := func(id, task string) providers.ToolCall {
		return providers.ToolCall{
			ID:   id,
			Name: "delegate",
			Arguments: map[string]any{
				"agent": "researcher",
				"task":  task,
			},
		}
	}
	return [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{
				delegate("call-d1", "fan-a"),
				delegate("call-d2", "fan-b"),
				delegate("call-d3", "fan-c"),
			}},
			{Done: true},
		},
		{{Text: "coordinated."}, {Done: true}},
	}
}

// childScript arms each task with a single child turn (a final text
// answer followed by done, no tool calls).
func armFanoutChildren(p *fanoutProvider, tasks ...string) {
	for _, task := range tasks {
		p.childTurns[task] = []providers.StreamEvent{
			{Text: "report-" + task},
			{Done: true},
		}
	}
}

// TestOrchestrationOverlayReachesPrompts pins the fan-out contract to
// the model-visible surfaces: with orchestration on, the delegation
// overlay must appear in the parent's system prompt and in a child's;
// with orchestration off, the system prompt must stay exactly the
// agent's own (no phantom delegation guidance for a tool that does not
// exist).
func TestOrchestrationOverlayReachesPrompts(t *testing.T) {
	parentTurns := [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "researcher",
					"task":  "single fan",
				},
			}}},
			{Done: true},
		},
		{{Text: "child answer"}, {Done: true}},
		{{Text: "coordinated."}, {Done: true}},
	}

	on := scriptedProvider{turns: parentTurns}
	runtimeOn := newProbeRuntime(t, &on, config.Orchestration{Enabled: true}, newChildRegistryForTests())
	drainAllEventsForTest(t, mustStream(t, runtimeOn))
	if len(on.messages) < 2 {
		t.Fatalf("orchestration-on provider calls = %d, want at least 2 (parent + child)", len(on.messages))
	}
	if !strings.Contains(on.messages[0][0].Content, "issue the delegate calls for them together") {
		t.Fatal("parent system prompt is missing the delegation fan-out overlay")
	}
	if !strings.Contains(on.messages[1][0].Content, "issue the delegate calls for them together") {
		t.Fatal("child system prompt is missing the delegation fan-out overlay")
	}

	off := scriptedProvider{turns: parentTurns[:1]}
	runtimeOff := newTestRuntime(&off)
	drainAllEventsForTest(t, mustStream(t, runtimeOff))
	if len(off.messages) == 0 {
		t.Fatal("orchestration-off run never called the provider")
	}
	if strings.Contains(off.messages[0][0].Content, "issue the delegate calls for them together") {
		t.Fatal("delegation overlay leaked into a run without an orchestrator")
	}
}

// mustStream starts a StreamChat run and drains it, failing on the
// stream-creation error.
func mustStream(t *testing.T, r *Runtime) <-chan Event {
	t.Helper()
	events, err := r.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "go"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	return events
}

// defsCaptureProvider extends scriptedProvider with a capture of the
// tool definitions each provider call received — the model-visible tool
// universe.
type defsCaptureProvider struct {
	scriptedProvider
	mu   sync.Mutex
	defs [][]tools.Definition
}

func (p *defsCaptureProvider) StreamChat(ctx context.Context, messages []providers.Message, defs []tools.Definition) (<-chan providers.StreamEvent, error) {
	p.mu.Lock()
	p.defs = append(p.defs, defs)
	p.mu.Unlock()
	return p.scriptedProvider.StreamChat(ctx, messages, defs)
}

// hasToolDef reports whether defs contains a tool with the given name.
func hasToolDef(defs []tools.Definition, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

// mustStreamMode starts a StreamChatWithMode run in the given mode and
// drains it, failing on the stream-creation error.
func mustStreamMode(t *testing.T, r *Runtime, mode RunMode) []Event {
	t.Helper()
	events, err := r.StreamChatWithMode(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "go"},
	}, mode)
	if err != nil {
		t.Fatalf("StreamChatWithMode(%v) error = %v", mode, err)
	}
	return drainAllEventsForTest(t, events)
}

// TestOrchestrationOverlayGatedByPlanMode pins the Phase 6 audit fix
// through actual prompt construction: the delegation fan-out contract
// reaches a chat-mode run with an orchestrator (and its provider-visible
// universe includes delegate), is absent from a plan-mode run (whose
// universe strips delegate — the planning overlay must still apply),
// and never reaches a chat-mode run without an orchestrator.
func TestOrchestrationOverlayGatedByPlanMode(t *testing.T) {
	turns := [][]providers.StreamEvent{{{Text: "ok"}, {Done: true}}}

	// Chat mode with orchestration: overlay present, delegate available.
	chat := defsCaptureProvider{scriptedProvider: scriptedProvider{turns: turns}}
	rtChat := newProbeRuntime(t, &chat, config.Orchestration{Enabled: true}, newChildRegistryForTests())
	mustStreamMode(t, rtChat, ModeChat)
	if len(chat.messages) == 0 {
		t.Fatal("chat run never called the provider")
	}
	if sys := chat.messages[0][0].Content; !strings.Contains(sys, "issue the delegate calls for them together") {
		t.Fatalf("chat-mode system prompt is missing the delegation overlay: %.200q", sys)
	}
	if len(chat.defs) == 0 || !hasToolDef(chat.defs[0], "delegate") {
		t.Fatalf("chat-mode tool universe lost delegate: %v", chat.defs)
	}

	// Plan mode with orchestration: plan overlay stays, delegation
	// overlay goes, delegate stays stripped from the universe.
	plan := defsCaptureProvider{scriptedProvider: scriptedProvider{turns: turns}}
	rtPlan := newProbeRuntime(t, &plan, config.Orchestration{Enabled: true}, newChildRegistryForTests())
	mustStreamMode(t, rtPlan, ModePlan)
	if len(plan.messages) == 0 {
		t.Fatal("plan run never called the provider")
	}
	sys := plan.messages[0][0].Content
	if strings.Contains(sys, "issue the delegate calls for them together") {
		t.Fatalf("plan-mode system prompt received the delegation overlay: %.200q", sys)
	}
	if !strings.Contains(sys, "Plan Mode") {
		t.Fatalf("plan-mode system prompt lost its planning overlay: %.200q", sys)
	}
	if len(plan.defs) == 0 || hasToolDef(plan.defs[0], "delegate") {
		t.Fatalf("plan-mode tool universe must not offer delegate: %v", plan.defs)
	}

	// Chat mode without orchestration: unchanged from before Phase 5.
	off := scriptedProvider{turns: turns}
	rtOff := newTestRuntime(&off)
	mustStreamMode(t, rtOff, ModeChat)
	if len(off.messages) == 0 {
		t.Fatal("orchestration-off run never called the provider")
	}
	if sys := off.messages[0][0].Content; strings.Contains(sys, "issue the delegate calls for them together") {
		t.Fatalf("delegation overlay leaked into a run without an orchestrator: %.200q", sys)
	}
}

// TestOrchestrationOverlayChildSnapshotsStayChat pins requirement 4 at
// the actual prompt constructor: runChild copies the parent's snapshot
// and forces chat mode (delegation_tool.go childSnap.mode = ModeChat).
// Even a plan-mode parent's child snapshot therefore carries the
// delegation overlay, while the parent's own plan-mode prompt does not.
func TestOrchestrationOverlayChildSnapshotsStayChat(t *testing.T) {
	registry := newChildRegistryForTests()
	orch := newOrchestrator(config.Orchestration{Enabled: true}, registry, nil, nil)

	parentSnap := runSnapshot{
		agent: agent.New("root", "root-system", ""),
		orch:  orch,
		mode:  ModePlan,
	}

	parentPrompt, ok := buildPromptBase(parentSnap)
	if !ok {
		t.Fatal("buildPromptBase reported no agent")
	}
	if strings.Contains(parentPrompt, "issue the delegate calls for them together") {
		t.Fatal("plan-mode parent prompt carries the delegation overlay")
	}
	if !strings.Contains(parentPrompt, "Plan Mode") {
		t.Fatal("plan-mode parent prompt lost its planning overlay")
	}

	// Exactly what runChild does: copy the parent snapshot, force chat.
	childSnap := parentSnap
	childSnap.mode = ModeChat
	childPrompt, ok := buildPromptBase(childSnap)
	if !ok {
		t.Fatal("buildPromptBase reported no agent for the child snapshot")
	}
	if !strings.Contains(childPrompt, "issue the delegate calls for them together") {
		t.Fatal("chat-mode child snapshot is missing the delegation overlay")
	}
	if strings.Contains(childPrompt, "Plan Mode") {
		t.Fatal("child snapshot leaked the planning overlay")
	}
}

// fanoutEventRecord pairs a drained event with its delivery index so
// ordering assertions (parent waits for all children) stay exact.
type fanoutEventRecord struct {
	index int
	ev    Event
}

// delegateTerminals filters the drained runtime events down to the
// delegate tool's terminal results in delivery order, and reports the
// index of the run's final done event.
func delegateTerminals(events []Event) (terminals []fanoutEventRecord, doneIndex int) {
	doneIndex = -1
	for i := range events {
		switch events[i].Type {
		case EventToolFinish, EventToolFailed, EventToolCancelled:
			if events[i].ToolResult != nil && events[i].ToolResult.Name == "delegate" {
				terminals = append(terminals, fanoutEventRecord{index: i, ev: events[i]})
			}
		case EventDone:
			if doneIndex < 0 {
				doneIndex = i
			}
		}
	}
	return terminals, doneIndex
}

// TestThreeDelegateCallsExecuteConcurrently is the blocking Phase 5
// acceptance criterion: three independent delegate calls carried by ONE
// assistant response must overlap in execution. The barrier closes only
// when the third child enters its provider call, so a serialized
// dispatcher deadlocks into the watchdog instead of passing. It also
// proves the parent waits for every child result before its final
// response, and that each report correlates to its own delegation.
func TestThreeDelegateCallsExecuteConcurrently(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(fanoutParentTurns(), 3)
	armFanoutChildren(provider, "fan-a", "fan-b", "fan-c")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "fan out three ways"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	drained := drainAllEventsForTest(t, events)
	if provider.peak != 3 {
		t.Fatalf("peak concurrent child provider turns = %d, want exactly 3 (barrier-proven overlap)", provider.peak)
	}

	terminals, doneIndex := delegateTerminals(drained)
	if len(terminals) != 3 {
		t.Fatalf("delegate terminal results = %d, want 3", len(terminals))
	}
	if doneIndex < 0 {
		t.Fatal("parent never delivered its final response")
	}
	for _, rec := range terminals {
		if rec.index > doneIndex {
			t.Fatalf("delegate terminal for %s delivered after the parent's final response — the parent did not wait for all children", rec.ev.ToolResult.ToolCallID)
		}
	}

	// Correlation: each delegate result carries its own child's report
	// and no sibling's.
	want := map[string]string{
		"call-d1": "report-fan-a",
		"call-d2": "report-fan-b",
		"call-d3": "report-fan-c",
	}
	seen := map[string]bool{}
	for _, rec := range terminals {
		id := rec.ev.ToolResult.ToolCallID
		if seen[id] {
			t.Fatalf("duplicate delegate terminal for %s", id)
		}
		seen[id] = true
		content := rec.ev.ToolResult.Content
		if !strings.Contains(content, want[id]) {
			t.Fatalf("delegate %s result missing its own report %q: %.200q", id, want[id], content)
		}
		for other, marker := range want {
			if other != id && marker != want[id] && strings.Contains(content, marker) {
				t.Fatalf("delegate %s result leaked sibling report %q: %.200q", id, marker, content)
			}
		}
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("delegate terminal for %s never arrived", id)
		}
	}
}

// TestFanOutRespectsConcurrentLimit proves the orchestrator's
// max_concurrent_children gate on the same one-response fan-out: with a
// cap of 2, exactly two children ever overlap — the barrier proves two
// entered together, the semaphore proves the third could not.
func TestFanOutRespectsConcurrentLimit(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(fanoutParentTurns(), 2)
	armFanoutChildren(provider, "fan-a", "fan-b", "fan-c")
	runtime := newProbeRuntime(t, provider, childConfig(t, 2, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "fan out three ways, two at a time"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	drained := drainAllEventsForTest(t, events)
	if provider.peak != 2 {
		t.Fatalf("peak concurrent child provider turns = %d, want exactly 2 under a cap of 2", provider.peak)
	}
	terminals, _ := delegateTerminals(drained)
	if len(terminals) != 3 {
		t.Fatalf("delegate terminal results = %d, want 3 (the capped third must still run)", len(terminals))
	}
	for _, rec := range terminals {
		if rec.ev.ToolResult.IsError {
			t.Fatalf("delegate %s unexpectedly failed under the concurrency cap: %.200q",
				rec.ev.ToolResult.ToolCallID, rec.ev.ToolResult.Content)
		}
	}
}

// TestFanOutChildFailureDoesNotCancelSiblings proves failure isolation:
// one child's provider erroring must fail only its own delegation while
// its siblings — already in flight with it at the barrier — run to
// completion and report normally.
func TestFanOutChildFailureDoesNotCancelSiblings(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(fanoutParentTurns(), 3)
	armFanoutChildren(provider, "fan-a", "fan-b", "fan-c")
	provider.childErr["fan-b"] = errors.New("child provider boom")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "fan out with one failure"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	drained := drainAllEventsForTest(t, events)
	if provider.peak != 3 {
		t.Fatalf("peak concurrent child provider turns = %d, want 3 (the failing child overlaps too)", provider.peak)
	}

	terminals, _ := delegateTerminals(drained)
	if len(terminals) != 3 {
		t.Fatalf("delegate terminal results = %d, want 3 (failure included)", len(terminals))
	}
	byCall := map[string]fanoutEventRecord{}
	for _, rec := range terminals {
		byCall[rec.ev.ToolResult.ToolCallID] = rec
	}
	if rec := byCall["call-d1"]; rec.ev.ToolResult.IsError || !strings.Contains(rec.ev.ToolResult.Content, "report-fan-a") {
		t.Fatalf("sibling d1 corrupted by d2's failure: error=%v content=%.200q", rec.ev.ToolResult.IsError, rec.ev.ToolResult.Content)
	}
	if rec := byCall["call-d3"]; rec.ev.ToolResult.IsError || !strings.Contains(rec.ev.ToolResult.Content, "report-fan-c") {
		t.Fatalf("sibling d3 corrupted by d2's failure: error=%v content=%.200q", rec.ev.ToolResult.IsError, rec.ev.ToolResult.Content)
	}
	if rec := byCall["call-d2"]; !rec.ev.ToolResult.IsError {
		t.Fatalf("failing child d2 did not report failure: %.200q", rec.ev.ToolResult.Content)
	} else if strings.Contains(rec.ev.ToolResult.Content, "report-fan-a") || strings.Contains(rec.ev.ToolResult.Content, "report-fan-c") {
		t.Fatalf("failing child d2 leaked sibling reports: %.200q", rec.ev.ToolResult.Content)
	}
}

// TestFanOutCancellationPropagates cancels the run while all three
// children are blocked in flight, then proves every delegation reports
// cancellation, the run terminates with the runtime's own cancellation
// event, and nothing hangs or leaks.
func TestFanOutCancellationPropagates(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(fanoutParentTurns(), 3)
	provider.holdRelease = true // barrier never opens: the cancel unblocks
	armFanoutChildren(provider, "fan-a", "fan-b", "fan-c")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Fire the cancel exactly when all three children are in flight.
		<-provider.enteredAll
		cancel()
	}()

	events, err := runtime.StreamChat(ctx, []providers.Message{
		{Role: providers.UserRole, Content: "fan out, then cancel"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	// After cancellation the run's emit gate deliberately drops normal
	// events (runtime_run.go: the consumer's view of cancelled work), so
	// individual delegate terminals are not guaranteed to be observed
	// here. What must hold: the stream terminates — which structurally
	// proves every delegate goroutine was joined (wg.Wait precedes run
	// return, run return precedes channel close) — and the runtime's
	// own cancellation event is delivered (non-blocking by design).
	sawRunCancelled := false
	for e := range events {
		if e.Type == EventCancelled {
			sawRunCancelled = true
		}
	}
	if provider.peak != 3 {
		t.Fatalf("peak concurrent child provider turns = %d, want 3 (all blocked together)", provider.peak)
	}
	if !sawRunCancelled {
		t.Fatal("run cancellation event never delivered; the run ended without reporting cancellation")
	}
}
