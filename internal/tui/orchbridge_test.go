package tui

// Phase 3 battery for tuiOrchSink: the bridge between orchestration
// worker goroutines and the bubbletea event loop. Conventions follow
// agentsstate_orch_test.go: no sleeps — determinism comes from joining
// the pump (pumpDone) and from gate channels that park it at known
// points; counters are asserted against exact conservation invariants.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/orchevents"

	tea "github.com/charmbracelet/bubbletea"
)

// --- helpers -------------------------------------------------------------------------

// collector returns a notify func recording every posted message and a
// snapshot func for assertions on the test goroutine.
func collector() (notify func(tea.Msg), snapshot func() []orchDeliverMsg) {
	var mu sync.Mutex
	var msgs []orchDeliverMsg
	notify = func(m tea.Msg) {
		mu.Lock()
		msgs = append(msgs, m.(orchDeliverMsg))
		mu.Unlock()
	}
	snapshot = func() []orchDeliverMsg {
		mu.Lock()
		defer mu.Unlock()
		return append([]orchDeliverMsg(nil), msgs...)
	}
	return notify, snapshot
}

// parkedSink returns a notify func whose first call records the batch
// and then parks the pump (blocking on a gate, like a stalled program),
// plus the entered signal, the release func and the snapshot. Emissions
// after the park therefore queue up without ever being drained.
func parkedSink() (notify func(tea.Msg), entered <-chan struct{}, release func(), snapshot func() []orchDeliverMsg) {
	gate := make(chan struct{})
	enteredC := make(chan struct{}, 1)
	var parked int32
	var mu sync.Mutex
	var msgs []orchDeliverMsg
	notify = func(m tea.Msg) {
		mu.Lock()
		msgs = append(msgs, m.(orchDeliverMsg))
		mu.Unlock()
		if atomic.CompareAndSwapInt32(&parked, 0, 1) {
			enteredC <- struct{}{}
			<-gate // pump parked mid-drainOnce until release
		}
	}
	snapshot = func() []orchDeliverMsg {
		mu.Lock()
		defer mu.Unlock()
		return append([]orchDeliverMsg(nil), msgs...)
	}
	return notify, enteredC, func() { close(gate) }, snapshot
}

// joinPump waits for the pump goroutine to exit after Close. The
// time.After is an emergency guard against a leaked pump, not a
// synchronization delay: no test relies on it firing.
func joinPump(t *testing.T, s *tuiOrchSink) {
	t.Helper()
	select {
	case <-s.pumpDone:
	case <-time.After(10 * time.Second):
		t.Fatal("pump goroutine did not exit after Close")
	}
}

// floodProgress builds a ChildProgress event: the agent name names the
// sender, turns counts that sender's sequence.
func floodProgress(sender, turn int) orchevents.Event {
	return orchChild(orchevents.ChildProgress,
		fmt.Sprintf("call-%d-%d", sender, turn),
		"general > coding", fmt.Sprintf("s%d", sender),
		fmt.Sprintf("run-%d-%d", sender, turn), 1, turn)
}

// drainEvents flattens recorded messages into one event slice, in
// delivery order.
func drainEvents(msgs []orchDeliverMsg) []orchevents.Event {
	var out []orchevents.Event
	for _, m := range msgs {
		out = append(out, m.events...)
	}
	return out
}

// accounting checks the sink's conservation invariant: every pre-Close
// emission was either posted or dropped, exactly once.
func accounting(t *testing.T, s *tuiOrchSink, emitted int) {
	t.Helper()
	got := s.deliveredLifeC + s.deliveredDrops + s.dropsDroppable + s.dropsHard
	if got != emitted {
		t.Fatalf("conservation: delivered=%d+%d drops=%d+%d, emitted %d",
			s.deliveredLifeC, s.deliveredDrops, s.dropsDroppable, s.dropsHard, emitted)
	}
}

func sinkQueueLen(s *tuiOrchSink) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// --- ordering and volume -------------------------------------------------------------

func TestSinkSingleSenderStrictOrder(t *testing.T) {
	notify, snapshot := collector()
	s := newTuiOrchSink(1, notify)
	for j := 1; j <= 200; j++ {
		s.Emit(floodProgress(7, j))
	}
	s.Close()
	joinPump(t, s)

	msgs := snapshot()
	events := drainEvents(msgs)
	if len(events) != 200 {
		t.Fatalf("delivered %d events, want 200", len(events))
	}
	for j, ev := range events {
		if want := j + 1; ev.Turns != want {
			t.Fatalf("event %d carries turn %d, want %d (order broken)", j+1, ev.Turns, want)
		}
	}
	// Coalescing bounds: at least one batch, never more messages than
	// emitted events.
	if len(msgs) < 1 || len(msgs) > 200 {
		t.Fatalf("batch count %d outside [1, 200]", len(msgs))
	}
	if s.dropsDroppable != 0 || s.dropsHard != 0 {
		t.Fatalf("no-pressure run dropped events: %d %d", s.dropsDroppable, s.dropsHard)
	}
	accounting(t, s, 200)
}

func TestSinkConcurrentEmitPerSenderOrder(t *testing.T) {
	notify, snapshot := collector()
	s := newTuiOrchSink(2, notify)
	const senders = 8
	const each = 200
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 1; j <= each; j++ {
				s.Emit(floodProgress(i, j))
			}
		}(i)
	}
	wg.Wait()
	s.Close()
	joinPump(t, s)

	events := drainEvents(snapshot())
	if len(events) != senders*each {
		t.Fatalf("delivered %d events, want %d", len(events), senders*each)
	}
	// The sink preserves append order, so each sender's subsequence
	// must arrive as exactly 1..each.
	seen := make(map[string]int)
	for _, ev := range events {
		tag := ev.Agent
		want := seen[tag] + 1
		if ev.Turns != want {
			t.Fatalf("sender %q delivered turn %d where %d was expected (reordered/lost)", tag, ev.Turns, want)
		}
		seen[tag] = ev.Turns
	}
	for i := 0; i < senders; i++ {
		if got := seen[fmt.Sprintf("s%d", i)]; got != each {
			t.Fatalf("sender %d last delivered turn %d, want %d", i, got, each)
		}
	}
	if s.dropsDroppable != 0 || s.dropsHard != 0 {
		t.Fatalf("no-pressure run dropped events: %d %d", s.dropsDroppable, s.dropsHard)
	}
	accounting(t, s, senders*each)
}

// --- lifecycle and shutdown ----------------------------------------------------------

func TestSinkCloseIsIdempotentAndPostCloseEmitHarmless(t *testing.T) {
	notify, snapshot := collector()
	s := newTuiOrchSink(3, notify)
	s.Emit(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		Agent:      "early",
		Chain:      "general > coding",
		Depth:      1,
		CallID:     "call-e",
		ChildRunID: "run-early",
	})
	s.Close()
	s.Close() // idempotent: closing done twice would panic if not

	// Post-close emits are silent no-ops, not drops and not queued.
	s.Emit(floodProgress(1, 1))
	joinPump(t, s)

	if q := sinkQueueLen(s); q != 0 {
		t.Fatalf("queue holds %d events after close", q)
	}
	events := drainEvents(snapshot())
	if len(events) != 1 || events[0].ChildRunID != "run-early" {
		t.Fatalf("delivered %v, want exactly the pre-Close spawn", events)
	}
	accounting(t, s, 1) // the post-Close emit is counted nowhere
}

func TestSinkCloseRacingEmitNeverPanics(t *testing.T) {
	notify, _ := collector()
	s := newTuiOrchSink(4, notify)
	const emitters = 4
	const each = 500
	var wg sync.WaitGroup
	var emitted int64
	for i := 0; i < emitters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 1; j <= each; j++ {
				atomic.AddInt64(&emitted, 1)
				s.Emit(floodProgress(i, j))
			}
		}(i)
	}
	// Close races the emitters exactly once (the goroutine), then a
	// second Close from the test goroutine exercises idempotence.
	go s.Close()
	wg.Wait()
	s.Close()
	joinPump(t, s)

	s.Emit(floodProgress(8, 1)) // post-close: no-op
	if q := sinkQueueLen(s); q != 0 {
		t.Fatalf("queue holds %d events after close", q)
	}
	// Emissions that landed before Close are all accounted for; ones
	// after it are not counted at all, so the invariant is an upper
	// bound here.
	if n := int64(s.deliveredLifeC + s.deliveredDrops + s.dropsDroppable + s.dropsHard); n > emitted {
		t.Fatalf("counted %d events, emitted %d", n, emitted)
	}
}

func TestSinkGenIsolation(t *testing.T) {
	notify, snapshot := collector()
	a := newTuiOrchSink(42, notify)
	b := newTuiOrchSink(43, notify)

	a.Emit(floodProgress(1, 1))
	b.Emit(floodProgress(2, 1))
	a.Close()
	b.Close()
	joinPump(t, a)
	joinPump(t, b)

	msgs := snapshot()
	perGen := map[uint64]int{}
	for _, m := range msgs {
		if len(m.events) == 0 {
			t.Fatal("empty batch posted")
		}
		if m.gen != 42 && m.gen != 43 {
			t.Fatalf("unexpected gen %d", m.gen)
		}
		perGen[m.gen]++
		for _, ev := range m.events {
			if want := fmt.Sprintf("s%d", m.gen-41); ev.Agent != want {
				t.Fatalf("gen %d delivered event tagged %q, want %q", m.gen, ev.Agent, want)
			}
		}
	}
	if perGen[42] < 1 || perGen[43] < 1 {
		t.Fatalf("both generations must deliver: %v", perGen)
	}
	if got := len(drainEvents(msgs)); got != 2 {
		t.Fatalf("delivered %d events, want 2", got)
	}
}

// --- pressure and drop policy --------------------------------------------------------

func TestSinkLifecycleSurvivesPressureAndClose(t *testing.T) {
	notify, entered, release, snapshot := parkedSink()
	s := newTuiOrchSink(7, notify)

	// The pump wakes, drains this first batch and parks inside its
	// notify: from here the queue is empty and stays undrained.
	s.Emit(floodProgress(0, 1))
	<-entered
	if q := sinkQueueLen(s); q != 0 {
		t.Fatalf("queue held %d events while the pump was parked", q)
	}

	// Flood past the cap: the queue pins at orchQueueLimit and every
	// surplus progress event evicts the oldest queued one.
	const flood = orchQueueLimit + 104
	for j := 1; j <= flood; j++ {
		s.Emit(floodProgress(3, j))
	}
	s.mu.Lock()
	q1, dD, dH := len(s.queue), s.dropsDroppable, s.dropsHard
	s.mu.Unlock()
	if q1 != orchQueueLimit {
		t.Fatalf("queue = %d, want pinned cap %d", q1, orchQueueLimit)
	}
	if dD != flood-orchQueueLimit || dH != 0 {
		t.Fatalf("pre-marker drops: droppable=%d hard=%d (want %d, 0)", dD, dH, flood-orchQueueLimit)
	}

	// A completion is lifecycle: it must displace a progress event,
	// never be shed and never counted as a hard drop.
	marker := orchChild(orchevents.ChildCompleted, "call-mark", "general > coding", "coding", "run-mark", 1, 99)
	marker.Outcome = orchevents.OutcomeDone
	s.Emit(marker)
	s.mu.Lock()
	q2, dD2, dH2 := len(s.queue), s.dropsDroppable, s.dropsHard
	s.mu.Unlock()
	if q2 != orchQueueLimit {
		t.Fatalf("queue after marker = %d, want cap", q2)
	}
	if dD2 != flood-orchQueueLimit+1 {
		t.Fatalf("dropsDroppable = %d, want %d", dD2, flood-orchQueueLimit+1)
	}
	if dH2 != 0 {
		t.Fatalf("dropsHard = %d, want 0", dH2)
	}

	// Release the gate, close and join: the pump drains everything
	// queued before Close, including the parked first batch.
	release()
	s.Close()
	joinPump(t, s)

	events := drainEvents(snapshot())
	markerSeen, progress := false, 0
	for _, ev := range events {
		if ev.Type == orchevents.ChildCompleted {
			if ev.ChildRunID != "run-mark" {
				t.Fatalf("wrong completion delivered: %s", ev.ChildRunID)
			}
			markerSeen = true
		} else {
			progress++
		}
	}
	if !markerSeen {
		t.Fatal("lifecycle completion lost under queue pressure")
	}
	wantProgress := 1 + (flood - (flood - orchQueueLimit + 1)) // first batch + flood survivors
	if progress != wantProgress {
		t.Fatalf("delivered %d progress events, want %d", progress, wantProgress)
	}
	if s.deliveredLifeC != 1 {
		t.Fatalf("deliveredLifeC = %d, want exactly the marker", s.deliveredLifeC)
	}
	if s.dropsHard != 0 {
		t.Fatalf("dropsHard = %d, want 0", s.dropsHard)
	}
	accounting(t, s, flood+2)
}

func TestSinkHardDropsOnlyWhenNoDroppables(t *testing.T) {
	notify, entered, release, snapshot := parkedSink()
	s := newTuiOrchSink(9, notify)

	spawn := func(id string) orchevents.Event {
		return orchChild(orchevents.AgentSpawned, "call-"+id, "general > coding", "coding", "run-"+id, 1, 0)
	}

	// Park the pump, then fill the queue entirely with lifecycle
	// events: nothing droppable for the next emit to evict.
	s.Emit(spawn("first"))
	<-entered
	for j := 0; j < orchQueueLimit; j++ {
		s.Emit(spawn(fmt.Sprintf("r%d", j)))
	}
	s.Emit(spawn("last")) // one lifecycle too many: the only hard-drop path
	s.mu.Lock()
	q, dH, dD := len(s.queue), s.dropsHard, s.dropsDroppable
	s.mu.Unlock()
	if q != orchQueueLimit || dH != 1 || dD != 0 {
		t.Fatalf("queue=%d drops hard=%d droppable=%d, want cap/1/0", q, dH, dD)
	}

	release()
	s.Close()
	joinPump(t, s)

	if got := len(drainEvents(snapshot())); got != orchQueueLimit+1 {
		t.Fatalf("delivered %d events, want %d (first + full queue)", got, orchQueueLimit+1)
	}
	if s.deliveredLifeC != orchQueueLimit+1 {
		t.Fatalf("deliveredLifeC = %d, want %d", s.deliveredLifeC, orchQueueLimit+1)
	}
	accounting(t, s, orchQueueLimit+2)
}

func TestSinkDrainsWithoutProgram(t *testing.T) {
	// A model without a live program must still drain: the pump is
	// the join point, and counters record what was extracted.
	s := newTuiOrchSink(11, nil)
	s.Emit(floodProgress(1, 1))
	s.Close()
	joinPump(t, s)
	if s.deliveredDrops != 1 || s.deliveredLifeC != 0 {
		t.Fatalf("extracted counters: %d+%d, want 1+0", s.deliveredLifeC, s.deliveredDrops)
	}
	accounting(t, s, 1)
}

func TestSinkDroppableClassification(t *testing.T) {
	progress := orchChild(orchevents.ChildProgress, "c", "ch", "a", "r", 1, 1)
	if !droppable(progress) {
		t.Fatal("progress must be droppable")
	}
	for _, ty := range []orchevents.Type{
		orchevents.ToolStarted, orchevents.ToolCompleted,
		orchevents.ToolFailed, orchevents.ToolDenied, orchevents.ToolCancelled,
	} {
		if !droppable(orchevents.Event{Type: ty}) {
			t.Fatalf("%s must be droppable", ty)
		}
	}
	for _, ty := range []orchevents.Type{
		orchevents.DelegationRequested, orchevents.DelegationAccepted,
		orchevents.DelegationRejected, orchevents.AgentSpawned,
		orchevents.ChildCompleted, orchevents.BudgetExhausted,
	} {
		if droppable(orchevents.Event{Type: ty}) {
			t.Fatalf("%s must never be droppable", ty)
		}
	}
}
