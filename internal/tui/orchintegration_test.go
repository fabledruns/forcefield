package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// Phase 4 integration tests: the opening animation, the quadrant
// canvas, and the orch event bridge wired into the real model Update /
// View paths. Everything is deterministic — animation time comes from
// explicit timestamps, event delivery from explicit messages, and the
// only waits are the pump-join guards shared with the Phase 3 suite.

// newOrchTestModel builds an orch-enabled model the way Start does for
// an orchestration-enabled config, with a real sink wired to a
// collector. The sink is closed on cleanup so the pump never leaks.
func newOrchTestModel(t *testing.T) (model, func() []orchDeliverMsg) {
	t.Helper()
	m := newStartingModel(&config.Config{}, session.New(), nil)
	m.orchEnabled = true
	notify, snapshot := collector()
	m.orchSink = newTuiOrchSink(m.orchGen, notify)
	t.Cleanup(func() { m.orchSink.Close() })
	return m, snapshot
}

// readyOrchModel returns an orch model after the first WindowSizeMsg
// (ready/layout, which no longer starts anything) and after
// orchestration activated the way production does: the stream feed's
// delegate ToolStart creates the queued row, then the orch feed's
// AgentSpawned — emitted only for an accepted, admitted delegation —
// latches activation, and a progress event runs the child. Returns the
// model plus the animation origin time.
func readyOrchModel(t *testing.T, w, h int) (model, time.Time) {
	t.Helper()
	m, _ := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = next.(model)
	m = activateOrch(t, m, "call-1", "run-1")
	if !m.anim.Started() {
		t.Fatal("activation did not latch the animation origin")
	}
	return m, m.anim.startTime
}

// activateOrch drives one child into execution and asserts the
// activation transition. callID creates the stream-feed row; childRunID
// is the child's fresh UUID for the structured feed.
func activateOrch(t *testing.T, m model, callID, childRunID string) model {
	t.Helper()
	next, _ := m.Update(streamEventMsg{Event: delegateStart(callID), gen: m.streamGen})
	m = next.(model)
	next, _ = m.Update(orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		orchChild(orchevents.AgentSpawned, callID, "general > coding", "coding", childRunID, 1, 0),
	}})
	m = next.(model)
	if !m.orchActive {
		t.Fatalf("AgentSpawned for %s did not activate the orchestration view", callID)
	}
	return m
}

// deliver applies one orchDeliverMsg through Update.
func deliver(t *testing.T, m model, msg orchDeliverMsg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model)
}

// --- animation lifecycle -------------------------------------------------------------

func TestOrchAnimStartsAtActivationAndSchedulesTick(t *testing.T) {
	m, _ := newOrchTestModel(t)
	if m.anim.Started() {
		t.Fatal("animation started at startup")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	if m.anim.Started() {
		t.Fatal("animation started on resize without an active delegation")
	}
	// Activation: a delegate call starts on the stream feed, then its
	// AgentSpawned arrives on the orch feed — the production transition.
	next, _ = m.Update(streamEventMsg{Event: delegateStart("call-1"), gen: m.streamGen})
	m = next.(model)
	next, cmd := m.Update(orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0),
	}})
	m = next.(model)
	if !m.orchActive {
		t.Fatal("AgentSpawned did not activate the orchestration session")
	}
	if !m.anim.Started() {
		t.Fatal("animation did not start at activation")
	}
	if cmd == nil {
		t.Fatal("first tick was not scheduled")
	}
	// The scheduled command must produce an openingTickMsg (its 33ms
	// fire is tea.Tick's normal behavior, not a test delay).
	if _, ok := cmd().(openingTickMsg); !ok {
		t.Fatal("scheduled command is not an opening tick")
	}
}

func TestOrchAnimResizeDoesNotRestartOrStackTicks(t *testing.T) {
	m, start := readyOrchModel(t, 100, 30)
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(model)
	if !m.anim.startTime.Equal(start) {
		t.Fatal("resize restarted or shifted the animation origin")
	}
	if cmd != nil {
		t.Fatal("resize stacked a second tick chain")
	}
	// The original chain is still alive: a tick mid-animation
	// schedules its successor.
	next, cmd = m.Update(openingTickMsg(start.Add(500 * time.Millisecond)))
	m = next.(model)
	if cmd == nil {
		t.Fatal("mid-animation tick did not schedule the next frame")
	}
}

func TestOrchAnimTicksAdvanceThenStopAtCompletion(t *testing.T) {
	m, start := readyOrchModel(t, 100, 30)

	// Mid-animation: the frame advances and the chain continues.
	next, cmd := m.Update(openingTickMsg(start.Add(2 * time.Second)))
	m = next.(model)
	if m.anim.Done() {
		t.Fatal("animation completed at 2s")
	}
	if m.anim.elapsed != 2*time.Second {
		t.Fatalf("elapsed = %v, want exactly the tick timestamp's delta", m.anim.elapsed)
	}
	if cmd == nil {
		t.Fatal("tick chain broke before completion")
	}

	// Inside the fade window the chain is still alive.
	next, cmd = m.Update(openingTickMsg(start.Add(10*time.Second + 300*time.Millisecond)))
	m = next.(model)
	if m.anim.Done() || cmd == nil {
		t.Fatal("fade window broke the tick chain")
	}

	// One large jump past the 10.6s boundary lands in the terminal
	// state in a single step and schedules nothing further.
	next, cmd = m.Update(openingTickMsg(start.Add(15 * time.Second)))
	m = next.(model)
	if !m.anim.Done() {
		t.Fatal("large time jump did not complete the animation")
	}
	if cmd != nil {
		t.Fatal("tick scheduled after the completion boundary")
	}

	// A late or duplicated tick after completion is inert.
	next, cmd = m.Update(openingTickMsg(start.Add(16 * time.Second)))
	if cmd != nil {
		t.Fatal("tick scheduled after completion")
	}
	_ = next
}

func TestOrchAnimDisabledStartsNothing(t *testing.T) {
	// config.Config{} keeps the default: orchestration off.
	m := newStartingModel(&config.Config{}, session.New(), nil)
	if m.orchEnabled {
		t.Fatal("orchEnabled inferred true without configuration")
	}
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	if m.anim.Started() {
		t.Fatal("animation started while the feature is disabled")
	}
	if cmd != nil {
		t.Fatal("tick scheduled while the feature is disabled")
	}
	// A structured event while disabled must neither activate nor
	// animate — a stray tick (never scheduled in production) is inert
	// too.
	next, cmd = m.Update(orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0),
	}})
	m = next.(model)
	if m.orchActive {
		t.Fatal("AgentSpawned activated the orchestration view while disabled")
	}
	if m.anim.Started() {
		t.Fatal("animation started while the feature is disabled")
	}
	next, cmd = m.Update(openingTickMsg(time.Now()))
	_ = next
	if cmd != nil {
		t.Fatal("stray tick scheduled work while disabled")
	}
}

func TestOrchAnimDoesNotBlockInput(t *testing.T) {
	m, _ := readyOrchModel(t, 100, 30)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	m = next.(model)
	if m.input.Value() != "hi" {
		t.Fatalf("input dead during animation: %q", m.input.Value())
	}
	if !m.anim.Started() || m.anim.Done() {
		t.Fatal("input disturbed the animation state")
	}
}

// --- event delivery and generation ---------------------------------------------------

func TestOrchDeliveryAppliesEventsAndAssignsSlots(t *testing.T) {
	m, _ := readyOrchModel(t, 100, 30)
	next, _ := m.Update(streamEventMsg{Event: delegateStart("call-1"), gen: m.streamGen})
	m = next.(model)
	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 3),
	}})
	if len(m.agents.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(m.agents.rows))
	}
	row := m.agents.rows[0]
	if row.State != AgentRunning || row.Turns != 3 {
		t.Fatalf("row = %s/%d turns, want running/3", row.State, row.Turns)
	}
	if m.agents.slots[0] != row {
		t.Fatal("running child did not claim the first slot")
	}
}

func TestOrchStaleGenerationRejected(t *testing.T) {
	// No activation: the reset happens on an idle session, exactly as
	// /new does before any delegation.
	m, _ := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	m.bumpOrchGen() // as /new, /clear, and session switch do
	if m.orchGen != 1 {
		t.Fatalf("orchGen = %d, want 1", m.orchGen)
	}
	m = deliver(t, m, orchDeliverMsg{gen: 0, events: []orchevents.Event{
		orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 3),
	}})
	if len(m.agents.rows) != 0 {
		t.Fatalf("stale batch mutated state: %d rows", len(m.agents.rows))
	}
	if m.orchActive {
		t.Fatal("stale batch activated the orchestration view")
	}
}

// TestOrchNewSessionDropsOldStragglersAndAcceptsNew reproduces the
// stale-generation problem end to end through the real sink: an event
// queued before the reset must be dropped on delivery, and events
// emitted after the reset must still deliver — the sink instance never
// changes, so no re-attach and no duplicate delivery is involved.
func TestOrchNewSessionDropsOldStragglersAndAcceptsNew(t *testing.T) {
	m, snapshot := newOrchTestModel(t)

	// Old execution: an event is emitted (and queued) before /new.
	m.orchSink.Emit(orchChild(orchevents.AgentSpawned, "call-old", "general > coding", "coding", "run-old", 1, 0))

	// /new resets the agents state and bumps the generation; the sink
	// learns it under its lock.
	if err := m.NewSession(); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if m.orchGen != 1 {
		t.Fatalf("orchGen = %d after /new, want 1", m.orchGen)
	}

	// The new execution delegates through the runtime feed (a fresh
	// stream carries gen 1 after NewSession's stopStream), and its
	// structured progress is emitted after the reset — stamped with
	// the new generation by the sink.
	next, _ := m.Update(streamEventMsg{Event: delegateStart("call-new"), gen: m.streamGen})
	m = next.(model)
	m.orchSink.Emit(orchChild(orchevents.ChildProgress, "call-new", "general > coding", "coding", "run-new", 1, 2))

	// Close and join so the collector holds both batches in emission
	// order (the pump is the single consumer, so ordering is exact).
	m.orchSink.Close()
	joinPump(t, m.orchSink)

	msgs := snapshot()
	if len(msgs) != 2 {
		t.Fatalf("delivered %d batches, want 2 (old + new)", len(msgs))
	}
	if msgs[0].gen == msgs[1].gen {
		t.Fatal("batches were not separated by generation")
	}
	if msgs[0].gen != 0 || msgs[1].gen != 1 {
		t.Fatalf("gens = %d, %d; want 0 then 1", msgs[0].gen, msgs[1].gen)
	}
	for _, msg := range msgs {
		m = deliver(t, m, msg)
	}
	if len(m.agents.rows) != 1 {
		t.Fatalf("rows = %d, want exactly the new run's row", len(m.agents.rows))
	}
	row := m.agents.rows[0]
	if row.CallID != "call-new" {
		t.Fatalf("surviving row = %q, want the post-reset run", row.CallID)
	}
	if row.State != AgentRunning || row.Turns != 2 {
		t.Fatalf("post-reset event not applied: %s/%d turns", row.State, row.Turns)
	}
}

func TestOrchClearDropsOldStragglers(t *testing.T) {
	m, snapshot := newOrchTestModel(t)
	m.orchSink.Emit(orchChild(orchevents.AgentSpawned, "call-old", "general > coding", "coding", "run-old", 1, 0))
	m.Clear()
	// The cleared session starts a fresh run (stream gen 1 after
	// Clear's stopStream); its structured progress is stamped with the
	// new generation.
	next, _ := m.Update(streamEventMsg{Event: delegateStart("call-new"), gen: m.streamGen})
	m = next.(model)
	m.orchSink.Emit(orchChild(orchevents.ChildProgress, "call-new", "general > coding", "coding", "run-new", 1, 2))
	m.orchSink.Close()
	joinPump(t, m.orchSink)

	for _, msg := range snapshot() {
		m = deliver(t, m, msg)
	}
	if len(m.agents.rows) != 1 || m.agents.rows[0].CallID != "call-new" {
		t.Fatalf("cleared state contaminated by old stragglers: %d rows", len(m.agents.rows))
	}
	if m.agents.rows[0].State != AgentRunning || m.agents.rows[0].Turns != 2 {
		t.Fatalf("post-clear event not applied: %s/%d", m.agents.rows[0].State, m.agents.rows[0].Turns)
	}
}

func TestOrchSwitchSessionBumpsGeneration(t *testing.T) {
	m, snapshot := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	next, _ = m.Update(streamEventMsg{Event: delegateStart("call-1"), gen: m.streamGen})
	m = next.(model)
	if len(m.agents.rows) != 1 {
		t.Fatal("precondition: row expected")
	}
	// switchToSession loads from disk, so persist the target first
	// (exactly what the /sessions picker's callers do).
	target := session.New()
	if err := target.Save(); err != nil {
		t.Fatalf("save target session: %v", err)
	}
	next, _ = m.switchToSession(target.ID)
	m = next.(model)
	if len(m.agents.rows) != 0 {
		t.Fatalf("session switch left %d rows", len(m.agents.rows))
	}
	if m.orchGen != 1 {
		t.Fatalf("orchGen = %d after switch, want 1", m.orchGen)
	}
	// A delayed message from the old generation cannot mutate the new
	// session's state, while a new-generation emission still delivers.
	m = deliver(t, m, orchDeliverMsg{gen: 0, events: []orchevents.Event{
		orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 9),
	}})
	if len(m.agents.rows) != 0 {
		t.Fatal("delayed old-generation message mutated the new session")
	}
	next, _ = m.Update(streamEventMsg{Event: delegateStart("call-2"), gen: m.streamGen})
	m = next.(model)
	m.orchSink.Emit(orchChild(orchevents.ChildProgress, "call-2", "general > coding", "coding", "run-2", 1, 4))
	m.orchSink.Close()
	joinPump(t, m.orchSink)
	for _, msg := range snapshot() {
		m = deliver(t, m, msg)
	}
	if len(m.agents.rows) != 1 || m.agents.rows[0].Turns != 4 {
		t.Fatalf("post-switch emission not delivered: %d rows", len(m.agents.rows))
	}
}

// TestOrchAttachDetachLifecycle verifies the model-level attach/detach
// ordering: attach on submit, detach on teardown, nothing when the
// feature is off. (The runtime's own registration semantics are
// covered by the runtime package's sink tests; a zero Runtime is
// enough here because SetOrchestrationEventSink is nil-safe.)
func TestOrchAttachDetachLifecycle(t *testing.T) {
	var events []bool
	orchAttachHook = func(attached bool) { events = append(events, attached) }
	t.Cleanup(func() { orchAttachHook = nil })

	m, _ := newOrchTestModel(t)
	m.runtime = &runtime.Runtime{}
	m.attachOrchSink()
	if len(events) != 1 || !events[0] {
		t.Fatalf("attach events = %v, want [true]", events)
	}
	m.stopStream(false)
	if len(events) != 2 || events[1] {
		t.Fatalf("detach events = %v, want [true false]", events)
	}
}

func TestOrchAttachDisabledIsNoop(t *testing.T) {
	var fired bool
	orchAttachHook = func(bool) { fired = true }
	t.Cleanup(func() { orchAttachHook = nil })

	m := newStartingModel(&config.Config{}, session.New(), nil)
	m.runtime = &runtime.Runtime{}
	m.attachOrchSink()
	m.detachOrchSink()
	if fired {
		t.Fatal("sink lifecycle ran while the feature is disabled")
	}
}

// --- queue pressure observability ----------------------------------------------------

func TestOrchHardDropNoticeReportsOncePerIncrease(t *testing.T) {
	m, _ := readyOrchModel(t, 100, 30)
	ev := orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 1)

	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{ev}, drops: orchDropSnapshot{droppable: 5, hard: 2}})
	if m.orchHardDrops != 2 {
		t.Fatalf("orchHardDrops = %d, want 2", m.orchHardDrops)
	}
	if n := countOccurrences(m, "lifecycle event"); n != 1 {
		t.Fatalf("notice count = %d, want exactly one after the first hard drop", n)
	}

	// The same snapshot again reports nothing new.
	before := len(m.entries)
	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{ev}, drops: orchDropSnapshot{droppable: 9, hard: 2}})
	if len(m.entries) != before {
		t.Fatal("droppable-only pressure or unchanged hard count produced a notice")
	}

	// A further hard drop reports once more.
	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{ev}, drops: orchDropSnapshot{droppable: 12, hard: 3}})
	if n := countOccurrences(m, "lifecycle event"); n != 2 {
		t.Fatalf("notice count = %d, want 2 after the second hard drop", n)
	}
}

// countOccurrences counts substring occurrences across entries —
// Println merges consecutive system entries into one, so a second
// notice can land inside the first entry's content.
func countOccurrences(m model, substr string) int {
	n := 0
	for _, e := range m.entries {
		n += strings.Count(e.Content, substr)
	}
	return n
}

// TestOrchSinkSplitBatchesAtGenerationBoundary extends the Phase 3
// suite: with a parked pump, emissions before and after a setGen must
// deliver as two same-generation batches in order, never mixed.
func TestOrchSinkSplitBatchesAtGenerationBoundary(t *testing.T) {
	notify, entered, release, snapshot := parkedSink()
	s := newTuiOrchSink(0, notify)

	s.Emit(orchChild(orchevents.AgentSpawned, "call-a", "general > coding", "coding", "run-a", 1, 0))
	s.Emit(orchChild(orchevents.AgentSpawned, "call-b", "general > coding", "coding", "run-b", 1, 0))
	<-entered // pump parked mid-drain; the queue is empty right now

	// Emissions after the park queue up: old gen first, then a reset,
	// then new-gen events.
	s.Emit(orchChild(orchevents.ChildProgress, "call-a", "general > coding", "coding", "run-a", 1, 1))
	s.setGen(1)
	s.Emit(orchChild(orchevents.ChildProgress, "call-c", "general > coding", "coding", "run-c", 1, 1))
	s.Emit(orchChild(orchevents.ChildProgress, "call-d", "general > coding", "coding", "run-d", 1, 2))

	release()
	s.Close()
	joinPump(t, s)

	msgs := snapshot()
	if len(msgs) < 2 {
		t.Fatalf("got %d batches, want at least old and new", len(msgs))
	}
	sawOld, sawNew := false, false
	for i, msg := range msgs {
		if msg.gen != 0 && msg.gen != 1 {
			t.Fatalf("batch %d carries gen %d", i, msg.gen)
		}
		for _, ev := range msg.events {
			if msg.gen == 0 && ev.CallID == "call-c" || msg.gen == 0 && ev.CallID == "call-d" {
				t.Fatalf("post-reset event %q delivered under the old generation", ev.CallID)
			}
			if msg.gen == 1 {
				sawNew = true
			} else {
				sawOld = true
			}
		}
	}
	if !sawOld || !sawNew {
		t.Fatalf("both generations must deliver: old=%v new=%v", sawOld, sawNew)
	}
}

// --- rendering integration -----------------------------------------------------------

func TestOrchViewRendersQuadrantsAndLineage(t *testing.T) {
	m, start := readyOrchModel(t, 100, 30)

	next, _ := m.Update(streamEventMsg{Event: delegateStart("call-1"), gen: m.streamGen})
	m = next.(model)
	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 3),
		// A nested grandchild: depth 2 under the anchored parent.
		orchChild(orchevents.ChildProgress, "call-2", "general > coding > nested", "nested", "run-2", 2, 1),
	}})

	// Advance the animation past the label stagger, then render.
	next, _ = m.Update(openingTickMsg(start.Add(2 * time.Second)))
	m = next.(model)
	view := m.View()

	for _, want := range []string{
		"main agent",
		"subagent-1",
		"subagent-2",
		"running",
		"↳ via subagent-1 (depth 2)",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("canvas missing %q:\n%s", want, view)
		}
	}
}

func TestOrchViewIdleSlotsShowStandby(t *testing.T) {
	// One child is executing (the activation transition), so exactly
	// two slots show standby. A zero-child canvas is not a reachable
	// state anymore: with nothing queued or running the view hands
	// back to the normal TUI.
	m, start := readyOrchModel(t, 100, 30)
	next, _ := m.Update(openingTickMsg(start.Add(2 * time.Second)))
	m = next.(model)
	view := m.View()
	if n := strings.Count(view, "idle · awaiting delegation"); n != 2 {
		t.Fatalf("standby lines = %d, want 2 with one child executing", n)
	}
}

func TestOrchViewOverflowAndArchivedCounts(t *testing.T) {
	m, start := readyOrchModel(t, 100, 30)

	// Build the slot story directly in the state the canvas reads, on
	// top of the activation row: r0 owns slot 1, goes terminal, is
	// displaced by r3 (archived), and r4 cannot claim a slot and lands
	// in the overflow summary.
	s := &m.agents
	now := start
	r0 := s.rows[0]
	slotRow(s, AgentRunning, now)
	slotRow(s, AgentRunning, now)
	finishRow(r0, AgentCompleted, now.Add(time.Second))
	r3 := slotRow(s, AgentRunning, now.Add(2*time.Second))
	r4 := slotRow(s, AgentRunning, now.Add(3*time.Second))
	r3.Turns, r4.Turns = 2, 1
	s.assignQuadrantSlots()

	next, _ := m.Update(openingTickMsg(start.Add(2 * time.Second)))
	m = next.(model)
	view := m.View()

	if !strings.Contains(view, "subagent-1 [1 archived]") {
		t.Fatalf("archived count missing from reclaimed slot:\n%s", view)
	}
	if !strings.Contains(view, "+1 running: subagent-5 (turn 1)") {
		t.Fatalf("overflow summary missing from Q3:\n%s", view)
	}
}

func TestOrchViewTerminalSummaryRenders(t *testing.T) {
	m, start := readyOrchModel(t, 100, 30)
	// A second child keeps the session active while the first completes:
	// a terminal summary must render in its slot, and the completed
	// child must not hand the view back while a sibling still runs.
	m = activateOrch(t, m, "call-2", "run-2")

	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		{Type: orchevents.ChildCompleted, CallID: "call-1", Chain: "general > coding",
			Agent: "coding", ChildRunID: "run-1", Depth: 1, Turns: 7, ToolCalls: 3,
			Outcome: orchevents.OutcomeDone, Status: "verification verified"},
	}})
	if !m.orchActive {
		t.Fatal("completed child hid the view while the sibling still runs")
	}
	next, _ := m.Update(openingTickMsg(start.Add(2 * time.Second)))
	m = next.(model)
	view := m.View()
	if !strings.Contains(view, "completed") || !strings.Contains(view, "verification verified") {
		t.Fatalf("terminal summary missing:\n%s", view)
	}
}

func TestOrchViewSmallTerminalsAreSafe(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{
		{Width: 40, Height: 10}, // stack mode
		{Width: 20, Height: 6},  // near-blank canvas
		{Width: 10, Height: 3},  // degenerate
	} {
		m, _ := newOrchTestModel(t)
		next, _ := m.Update(size)
		m = next.(model)
		// Activate before rendering: with nothing running the view is
		// the classic TUI, and the canvas panic-safety would go
		// unexercised.
		m = activateOrch(t, m, "call-1", "run-1")
		view := m.View()
		if view == "" {
			t.Fatalf("empty view at %dx%d", size.Width, size.Height)
		}
	}
}

func TestOrchViewPermissionOverlayIntact(t *testing.T) {
	m, start := readyOrchModel(t, 100, 30)
	next, _ := m.Update(openingTickMsg(start.Add(2 * time.Second)))
	m = next.(model)
	m.permissionPrompt = &permissionPrompt{}
	view := m.View()
	if !strings.Contains(view, "waiting for your answer") {
		t.Fatalf("permission footer lost in orchestration mode:\n%s", view)
	}
}

func TestOrchDisabledViewUnchanged(t *testing.T) {
	// Default config: orchestration off. The classic view must not
	// grow any canvas artifact.
	m := newStartingModel(&config.Config{}, session.New(), nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	next, _ = m.Update(streamEventMsg{Event: delegateStart("call-1"), gen: m.streamGen})
	m = next.(model)
	view := m.View()
	if strings.Contains(view, "awaiting delegation") {
		t.Fatal("canvas standby line leaked into the classic view")
	}
	if !strings.Contains(view, "subagent-1") {
		t.Fatal("classic agents panel lost its delegate row")
	}
}

// --- on-demand activation (Phase 5) ---------------------------------------------------

// TestOrchActivationRequiresAgentSpawned pins the activation gate: the
// delegation lifecycle up to (but not including) a child executing must
// never switch the view — configuration, tool availability, rejection,
// and pre-execution failure are all mundane states of the normal TUI —
// and AgentSpawned (accepted + admitted + child run starting) is the
// only activating event.
func TestOrchActivationRequiresAgentSpawned(t *testing.T) {
	m, _ := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)

	// Config enabled + resize: still the normal TUI.
	if m.orchActive || m.anim.Started() {
		t.Fatal("enabled configuration alone activated the orchestration view")
	}
	// A delegate call appearing on the stream feed (tool availability):
	// still not activation — the row is queued, nothing executes yet.
	next, _ = m.Update(streamEventMsg{Event: delegateStart("call-1"), gen: m.streamGen})
	m = next.(model)
	if m.orchActive {
		t.Fatal("delegate ToolStart alone activated the orchestration view")
	}
	// Rejected and requested leases never activate.
	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 42),
		orchLease(orchevents.DelegationRejected, "call-1", "general > coding", "coding", 0, 42),
	}})
	if m.orchActive {
		t.Fatal("delegation request/rejection activated the orchestration view")
	}
	if m.anim.Started() {
		t.Fatal("animation started without an executing child")
	}

	// The model retries with a fresh call: accepted, admitted, and the
	// child run starts — AgentSpawned, the one activating event.
	next, _ = m.Update(streamEventMsg{Event: delegateStart("call-2"), gen: m.streamGen})
	m = next.(model)
	next, _ = m.Update(orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		orchChild(orchevents.AgentSpawned, "call-2", "general > coding", "coding", "run-2", 1, 0),
	}})
	m = next.(model)
	if !m.orchActive {
		t.Fatal("AgentSpawned did not activate the orchestration view")
	}
	if !m.anim.Started() {
		t.Fatal("activation did not start the opening animation")
	}
}

// TestOrchDeactivatesWhenLastChildFinishes pins the hand-back: after the
// last non-terminal row goes terminal, the view returns to the normal
// TUI. Terminal rows keep their canvas summaries only while the session
// is still active (a sibling running); once nothing remains, the canvas
// must not linger.
func TestOrchDeactivatesWhenLastChildFinishes(t *testing.T) {
	m, _ := readyOrchModel(t, 100, 30)

	// Last child completes on the orch feed.
	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		{Type: orchevents.ChildCompleted, CallID: "call-1", Chain: "general > coding",
			Agent: "coding", ChildRunID: "run-1", Depth: 1, Turns: 3,
			Outcome: orchevents.OutcomeDone, Status: "done"},
	}})
	if m.orchActive {
		t.Fatal("orchestration view stayed active with no queued or running child")
	}
	if m.agents.active() {
		t.Fatal("agentsState still active after the last child finished")
	}
	// The classic view is back: no canvas artifacts.
	view := m.View()
	if strings.Contains(view, "idle · awaiting delegation") {
		t.Fatal("canvas standby line lingered after deactivation")
	}
}

// TestOrchStopStreamDeactivates covers the teardown path: cancelling
// the stream terminalizes open rows (cancelOpen) and must hand the view
// back even when the orch feed's terminal events never arrive (they are
// dropped by the run's emit gate after cancellation).
func TestOrchStopStreamDeactivates(t *testing.T) {
	m, _ := readyOrchModel(t, 100, 30)
	if !m.orchActive {
		t.Fatal("precondition: active session")
	}
	m.stopStream(false)
	if m.orchActive {
		t.Fatal("stopStream left the orchestration view active")
	}
	if m.agents.active() {
		t.Fatal("stopStream left rows open")
	}
}

// TestOrchAnimationOncePerSession pins the once-per-session opening:
// a second activation in the same orchestration session (another child
// spawning while the first runs) must not restart or shift the
// animation timeline — children never re-run the opening.
func TestOrchAnimationOncePerSession(t *testing.T) {
	m, start := readyOrchModel(t, 100, 30)
	m = activateOrch(t, m, "call-2", "run-2")
	if !m.anim.startTime.Equal(start) {
		t.Fatal("second activation restarted or shifted the animation origin")
	}
}

// TestOrchNewSessionResetsActivationAndAnimation pins the session
// boundary: /new deactivates, and the NEXT session's first activation
// re-runs the opening animation exactly once (the reset zeroed the
// animation state).
func TestOrchNewSessionResetsActivationAndAnimation(t *testing.T) {
	m, _ := readyOrchModel(t, 100, 30)
	if err := m.NewSession(); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if m.orchActive {
		t.Fatal("session switch left the orchestration view active")
	}
	if m.anim.Started() {
		t.Fatal("session switch kept the old animation origin")
	}
	// The new session's first activation re-runs the opening: a fresh
	// origin, later than the old one.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	m = activateOrch(t, m, "call-new", "run-new")
	if !m.anim.Started() {
		t.Fatal("new session's first activation did not start the animation")
	}
}

// TestOrchClearResetsActivationAndAnimation covers the /clear variant
// of the session reset.
func TestOrchClearResetsActivationAndAnimation(t *testing.T) {
	m, _ := readyOrchModel(t, 100, 30)
	m.Clear()
	if m.orchActive {
		t.Fatal("/clear left the orchestration view active")
	}
	if m.anim.Started() {
		t.Fatal("/clear kept the old animation origin")
	}
	m = activateOrch(t, m, "call-new", "run-new")
	if !m.anim.Started() {
		t.Fatal("post-clear activation did not start a fresh animation")
	}
}

// TestOrchStartupShowsNormalTUI pins the Phase 5 default surface: an
// orch-enabled model at startup — resized, no delegation yet — renders
// the classic transcript view with no canvas, no standby slots, and no
// animation.
func TestOrchStartupShowsNormalTUI(t *testing.T) {
	m, _ := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	if m.orchActive {
		t.Fatal("orchestration view active at startup")
	}
	if m.anim.Started() {
		t.Fatal("opening animation ran at startup")
	}
	view := m.View()
	if strings.Contains(view, "idle · awaiting delegation") {
		t.Fatal("canvas standby slots rendered at startup")
	}
	if strings.Contains(view, "awaiting delegation") {
		t.Fatal("canvas artifacts at startup")
	}
}
