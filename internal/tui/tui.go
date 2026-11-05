package tui

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/perfmark"
	"forcefield/internal/session"
	"forcefield/internal/terminal"
)

// tuiBrander sets the terminal title on startup and returns its restoration func.
// Package var so tests can observe or inject fakes without real terminal side effects.
var tuiBrander = terminal.ApplyBranding

// runProgram executes the Bubble Tea program. Package var so tests can stub
// the event loop without running a blocking interactive UI.
var runProgram = func(p *tea.Program) (tea.Model, error) {
	return p.Run()
}

func Start(sess *session.Session) error {
	restoreTitle := tuiBrander(terminal.DefaultBrandedTitle)
	defer restoreTitle()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	perfmark.Event("config-loaded")

	asker := &tuiAsker{}
	// TUI-first: render the first frame from the minimal startup model
	// immediately; the runtime (skills, memory, provider, tools, agents,
	// session alignment) initializes in the background via Init and
	// installs itself through runtimeReadyMsg.
	m := newStartingModel(cfg, sess, asker)

	// notify routes background results (model discovery) back into the
	// running event loop. The program variable is captured by reference
	// and assigned before Run starts, so Send is never called on a nil
	// program from work triggered after startup.
	var program *tea.Program
	m.notify = func(msg tea.Msg) {
		if program != nil {
			program.Send(msg)
		}
	}

	// The orchestration sink rides the same ownership model: built here
	// once per process, attached to the freshly built runtime inside
	// installRuntime, re-armed per run by the submit path, and closed
	// once the program stops. It exists only when the feature is on:
	// with orchestration disabled nothing attaches, nothing animates,
	// and the classic TUI keeps its exact behavior. Its pump uses
	// notify, so it never sends on a nil program and never races the
	// program pointer directly.
	if m.orchEnabled {
		m.orchSink = newTuiOrchSink(m.orchGen, m.notify)
	}

	program = tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	asker.program = program

	if m.orchSink != nil {
		// Deferred Close is the sink's teardown: after Run returns, no
		// program exists to receive, so the pump must stop and late
		// emissions must be discarded rather than parked forever.
		defer m.orchSink.Close()
	}

	_, err = runProgram(program)
	return err
}

// orchQueueLimit bounds the sink's queue. It is sized far above any
// legitimate run's event volume (the documented orchestration ceilings
// keep every run's lifecycle events in the low hundreds) so the bound
// only protects worst-case runaway memory.
const orchQueueLimit = 4096

// tuiOrchSink bridges the runtime's structured orchestration events
// into the bubbletea event loop. Its contract:
//
//   - Emit is called from orchestration goroutines, so it only appends
//     to a mutex-guarded queue and makes one non-blocking wake attempt;
//     it never touches the model, never blocks on terminal rendering,
//     and never calls the program directly (bubbletea's Send blocks on
//     its unbuffered message channel, which would stall scheduler
//     workers mid-Update).
//   - One pump goroutine per sink owns all postings. Each wake drains
//     the whole queue into a single orchDeliverMsg, which coalesces
//     progress bursts into one message and guarantees no queued event
//     is left behind before the pump waits again.
//   - While the TUI is active, lifecycle events are never lost: the
//     queue's overflow policy drops droppable progress/tool events
//     first and keeps every lifecycle event. After Close (the TUI has
//     exited), the pump posts everything queued before Close in one
//     final sweep — harmless, since a dead program's Send returns
//     immediately — and any late Emit call is a silent no-op.
//   - Channels are used so nothing can ever send on a closed one: the
//     coalescing wake channel is capped at one and never closed; the
//     done channel is closed exactly once by Close and never sent on.
//
// Generation safety (Phase 4): the sink lives for the whole process —
// the runtime keeps one Orchestrator, and /new, /clear, and session
// switches never rebuild it — so per-run sink replacement is not the
// mechanism. Instead every event is STAMPED with the current
// orchestration generation at Emit time, and the model's Update handler
// drops orchDeliverMsg batches whose stamps no longer match its live
// orchGen. bumpOrchGen advances the generation exactly where the model
// resets agents state (/clear, /new, session switch), so stragglers
// queued before the reset carry the old stamp and are dropped on
// delivery, while events emitted after it carry the new stamp and
// deliver normally — no re-attach, no duplicate-delivery window.
//
// Lifecycle guarantees (Phase 4 audit — conditionally safe verdict,
// preserved verbatim): the stamp is delivery gating, not attribution
// truth. A reset detaches the sink and bumps orchGen, but the runtime
// is not awaited: the next submit re-attaches (model_input.go, the
// submit path's attachOrchSink) without waiting for a still-unwinding
// old run to return, and the emitters (orchestration.go emitDelegation
// and emitChildRun, delegation_tool.go emitChildCompleted) deliberately
// do not gate on ctx — the unconditional child-completion event is what
// carries cancellation information to the parent. Within that window an
// old execution's remaining emissions ARE stamped with the newer
// generation and deliver into the new session. Containment does not
// come from the stamp: it comes from the correlation maps in
// agentsState.orchAnchor — an old event only mutates a row it can
// correlate by child run id, call id + chain, or (for nested events) a
// parent chain row; it cannot fabricate unrelated state. The residual
// risk is provider call-ID reuse: correlation assumes a provider never
// reuses a tool-call ID across runs. A provider that did could let an
// old unwind event anchor into a new delegation's row (same call id,
// same chain shape) — a narrow cross-run collision, documented rather
// than defended, because mainstream providers generate unique call ids.
// The Phase 4 audit additionally identified eight lifecycle scenarios
// around this window — reattach racing an unwinding run end to end,
// call-ID reuse collision, cross-feed first-terminal-wins under
// concurrent feeds, cancelOpen racing late structured events,
// drainCollapsed dedup across resets, bounded-queue drop accounting
// under mixed pressure, generation mismatch through the headless
// writer, and nested-event anchoring after a reset — that still lack
// dedicated adversarial tests; the verdict's containment argument rests
// on the correlation maps, not on test coverage of these paths.
type tuiOrchSink struct {
	// gen is the current orchestration session generation, guarded by
	// mu and stamped onto every event at Emit time; setGen advances it
	// under the same lock when the model resets session state.
	gen    uint64
	notify func(tea.Msg) // posts into the program; brief blocking is fine here

	// queue and counters are guarded by mu. Only the pump removes
	// entries; Emit appends and drops under the same lock.
	mu             sync.Mutex
	queue          []orchQueueEntry
	closed         bool
	dropsDroppable int // dropped progress/tool events (bounded-queue pressure)
	dropsHard      int // dropped lifecycle events (exhausted bound; ~never)
	deliveredLifeC int // lifecycle events posted to the program
	deliveredDrops int // droppable events posted to the program

	// wake coalesces wakes; never closed, so concurrent emitters can
	// always attempt to signal. done signals shutdown; never sent on,
	// only closed.
	wake chan struct{}
	done chan struct{}

	// pumpDone is closed exactly once when the pump goroutine exits,
	// so tests can join it without sampling goroutine counts.
	pumpDone chan struct{}
}

// orchQueueEntry pairs one queued event with the generation stamped
// when it was emitted. Keeping the stamp per entry (not per sink) is
// what lets one long-lived sink serve consecutive session generations
// without re-attachment or duplicate delivery.
type orchQueueEntry struct {
	gen uint64
	ev  orchevents.Event
}

// newTuiOrchSink starts the pump immediately; nothing is posted before
// the first Emit, which in practice only happens after installRuntime
// has attached the sink and orchestration is actually running.
func newTuiOrchSink(gen uint64, notify func(tea.Msg)) *tuiOrchSink {
	s := &tuiOrchSink{
		gen:      gen,
		notify:   notify,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
		pumpDone: make(chan struct{}),
	}
	go s.pump()
	return s
}

// droppable reports whether an event may be dropped under queue
// pressure. Progress and per-tool events are replaceable: the next
// progress carries cumulative turns, and ChildCompleted's exact
// counter snapshot reconciles whatever was hidden. Every lifecycle
// (identity or terminal) event is kept: losing a rejection, a spawn
// identity, or a completion would corrupt the panel's story.
func droppable(ev orchevents.Event) bool {
	switch ev.Type {
	case orchevents.ChildProgress,
		orchevents.ToolStarted, orchevents.ToolCompleted,
		orchevents.ToolFailed, orchevents.ToolDenied, orchevents.ToolCancelled:
		return true
	}
	return false
}

// Emit implements orchevents.Sink. It never observes the queue cap by
// blocking the caller: on pressure it drops the oldest droppable queued
// event, and only when the queue is entirely lifecycle events does it
// shed the incoming one (counted as hard drops; the orchestration
// ceilings keep that volume unreachable in practice).
func (s *tuiOrchSink) Emit(ev orchevents.Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if len(s.queue) >= orchQueueLimit {
		if i := s.indexOfDroppable(); i >= 0 {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.dropsDroppable++
		} else if droppable(ev) {
			s.dropsDroppable++
			s.mu.Unlock()
			return
		} else {
			s.dropsHard++
			s.mu.Unlock()
			return
		}
	}
	// Stamp the generation at emission time: whatever generation the
	// model has moved to by the time this event is delivered, the stamp
	// records which session the event belonged to when it happened.
	s.queue = append(s.queue, orchQueueEntry{gen: s.gen, ev: ev})
	s.mu.Unlock()

	// Coalescing wake: a pending signal is enough; the pump drains
	// everything queued before waiting again.
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// setGen advances the orchestration generation. Called from the model
// when session state is reset (/clear, /new, session switch), so every
// event emitted afterwards is stamped with the new generation while
// stragglers already queued keep the old one and are dropped on
// delivery. The lock is the queue's: stamps and queue contents change
// atomically with respect to extraction.
func (s *tuiOrchSink) setGen(gen uint64) {
	s.mu.Lock()
	s.gen = gen
	s.mu.Unlock()
}

// indexOfDroppable returns the oldest droppable event's index, or -1.
// The queue is at most orchQueueLimit long, so a linear scan under the
// queue lock is bounded work on a path that must not exist in practice.
func (s *tuiOrchSink) indexOfDroppable() int {
	for i, entry := range s.queue {
		if droppable(entry.ev) {
			return i
		}
	}
	return -1
}

// pump is the sink's single consumer goroutine. It wakes, drains the
// whole queue (possibly several batches as emissions keep arriving)
// and only then waits again, so progress bursts coalesce into few
// messages and nothing queued is ever skipped.
func (s *tuiOrchSink) pump() {
	defer close(s.pumpDone)
	for {
		select {
		case <-s.done:
			// Final sweep: post everything queued before Close. A
			// pending wake token could otherwise lose the race against
			// done in this select and strand queued events, and no
			// further appends can arrive (Emit is already closed), so
			// the queue only shrinks: this terminates.
			for s.drainOnce() {
			}
			return
		case <-s.wake:
			for s.drainOnce() {
			}
		}
	}
}

// drainOnce extracts one same-generation batch, if any, and posts it.
// Batches never mix generations: the queue is split at the first
// generation boundary, so a reset that lands between emission and
// delivery separates cleanly — old-generation entries stay queued
// behind the boundary for the model to drop, new-generation entries
// deliver without waiting for them. Extracting and posting stay inside
// the drain loop even when Close has already run: a closed sink's
// posts are quick no-ops in a dead program (Send returns as soon as
// its context is done), so close never strands a queued lifecycle
// event mid-drain. Caller must not hold mu.
func (s *tuiOrchSink) drainOnce() bool {
	s.mu.Lock()
	if len(s.queue) == 0 {
		s.mu.Unlock()
		return false
	}
	gen := s.queue[0].gen
	boundary := 1
	for boundary < len(s.queue) && s.queue[boundary].gen == gen {
		boundary++
	}
	batch := make([]orchevents.Event, 0, boundary)
	for _, entry := range s.queue[:boundary] {
		batch = append(batch, entry.ev)
	}
	if boundary == len(s.queue) {
		s.queue = nil
	} else {
		rest := make([]orchQueueEntry, len(s.queue)-boundary)
		copy(rest, s.queue[boundary:])
		s.queue = rest
	}
	drops := orchDropSnapshot{droppable: s.dropsDroppable, hard: s.dropsHard}
	s.countBatchLocked(batch)
	s.mu.Unlock()

	if s.notify == nil {
		// Models without a live program: events go nowhere (as outside
		// the TUI), but the pump still drains.
		return true
	}
	s.notify(orchDeliverMsg{gen: gen, events: batch, drops: drops})
	return true
}

// countBatchLocked book-marks what was handed to the program. Caller
// holds mu.
func (s *tuiOrchSink) countBatchLocked(batch []orchevents.Event) {
	for _, ev := range batch {
		if droppable(ev) {
			s.deliveredDrops++
		} else {
			s.deliveredLifeC++
		}
	}
}

// Close stops the pump and makes every later Emit a no-op. It is
// idempotent and does not join the pump: Close runs from Start's
// teardown path, and the pump drains the remaining queue once and
// exits on its own. Posting the pre-Close residue is safe after the
// program exits (bubbletea's Send is a fast no-op once its context is
// done); only emits issued after Close are discarded, by design.
func (s *tuiOrchSink) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
}

// orchAttachHook observes every runtime sink attach/detach. Nil in
// production; tests use it to prove per-run attach and teardown detach
// happen without a live program (same pattern as viewPhaseHook).
var orchAttachHook func(attached bool)

// attachOrchSink (re-)registers the model's sink with the runtime. The
// runtime keeps one Orchestrator per process and swaps sinks
// mutex-guarded, and the sink instance never changes, so re-attaching
// per run is safe: emissions always go to exactly one registration, so
// no attach can duplicate delivery. It is needed because stopStream
// detaches the sink while no run is in flight. No-op when the feature
// is off (no sink exists), before the runtime is installed, or outside
// a live TUI.
func (m *model) attachOrchSink() {
	if !m.orchEnabled || m.orchSink == nil || m.runtime == nil {
		return
	}
	m.runtime.SetOrchestrationEventSink(m.orchSink)
	if orchAttachHook != nil {
		orchAttachHook(true)
	}
}

// detachOrchSink clears the runtime's sink registration. stopStream
// calls it while no run is in flight so a cancelled run's teardown
// stragglers (a child completing just as its context dies) land in a
// nil sink and are dropped at the source — they can no longer be
// stamped with a newer generation after a session reset. Events already
// queued before the detach still deliver; their stamps predate the
// detach and the model's orchGen is unchanged by turn teardown.
func (m *model) detachOrchSink() {
	if m.orchSink == nil || m.runtime == nil {
		return
	}
	m.runtime.SetOrchestrationEventSink(nil)
	if orchAttachHook != nil {
		orchAttachHook(false)
	}
}

// bumpOrchGen advances the orchestration session generation. It must be
// called exactly where m.agents is reset wholesale (/clear, /new,
// session switch): queued stragglers stamped before the bump are
// dropped by the Update gate, and events emitted afterwards — including
// a brand-new run's — carry the new generation and deliver normally.
// This is deliberately NOT tied to streamGen: stopStream bumps that on
// every turn end, which would deafen the panel after the first turn.
// The animation state is zeroed at the same sites (in the callers), so
// the opening signature runs once per orchestration session and the
// session's first activation latches a fresh timeline.
func (m *model) bumpOrchGen() {
	m.orchGen++
	if m.orchSink != nil {
		m.orchSink.setGen(m.orchGen)
	}
}
