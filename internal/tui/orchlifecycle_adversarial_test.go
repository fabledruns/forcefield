package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// Scenario 1: Re-attach racing an unwinding run end to end.
// Run 1 starts and spawns a child. Session is reset (/new or /clear), bumping
// the generation and closing the old sink. Run 2 starts in the new session.
// The in-flight child from Run 1 finishes unwinding and emits its terminal event.
// The late event must NOT enter Run 2's sink or contaminate Run 2's session.
func TestOrchLifecycle_ReattachRacingUnwind(t *testing.T) {
	m, snapshot := newOrchTestModel(t)
	sink1 := m.orchSink

	// Run 1 child is admitted and spawned under gen 0.
	sink1.Emit(orchChild(orchevents.AgentSpawned, "call-r1", "general > coding", "coding", "run-r1", 1, 0))

	// User triggers /new: bumps orchGen to 1, closes sink1, creates sink2.
	if err := m.NewSession(); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if m.orchGen != 1 {
		t.Fatalf("orchGen after /new = %d, want 1", m.orchGen)
	}
	sink2 := m.orchSink
	if sink1 == sink2 {
		t.Fatal("NewSession must replace the sink with a new instance")
	}

	// Run 2 starts in the new session with a new delegation.
	next, _ := m.Update(streamEventMsg{Event: delegateStart("call-r2"), gen: m.streamGen})
	m = next.(model)
	sink2.Emit(orchChild(orchevents.AgentSpawned, "call-r2", "general > coding", "coding", "run-r2", 1, 0))

	// In-flight child from Run 1 now finishes unwinding and emits into its captured sink (sink1).
	sink1.Emit(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "exec-1",
		Agent:      "coding",
		Chain:      "general > coding",
		Depth:      1,
		CallID:     "call-r1",
		ChildRunID: "run-r1",
		Outcome:    orchevents.OutcomeCancelled,
		Error:      "context cancelled",
	})

	// Close sink2 and join pumps so all queued messages are delivered.
	sink1.Close()
	joinPump(t, sink1)
	sink2.Close()
	joinPump(t, sink2)

	// Apply every delivered batch through Update.
	for _, msg := range snapshot() {
		m = deliver(t, m, msg)
	}

	// Session 2 state must only contain Run 2's row.
	if len(m.agents.rows) != 1 {
		t.Fatalf("agents panel has %d rows, want 1 (contaminated by Run 1)", len(m.agents.rows))
	}
	if m.agents.rows[0].CallID != "call-r2" {
		t.Fatalf("surviving row = %q, want call-r2", m.agents.rows[0].CallID)
	}
}

// Scenario 2: Cross-run call-ID reuse collision.
// A provider reuses a tool-call ID (e.g. "call-1") across Run 1 and Run 2.
// Events from Run 1 must not anchor into Run 2's row, even with identical
// call IDs and chains. Execution identity (RunID) isolates the rows.
func TestOrchLifecycle_CallIDReuseCollision(t *testing.T) {
	var s agentsState

	// Run 1: provider uses call-1.
	s.startDelegate(&providers.ToolCall{
		ID:        "call-1",
		Arguments: map[string]any{"agent": "coding", "task": "run 1 task"},
	})
	if len(s.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(s.rows))
	}
	row1 := s.rows[0]

	// Anchor row1 with RunID = "run-1".
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "run-1",
		Agent:      "coding",
		Chain:      "root > coding",
		Depth:      1,
		CallID:     "call-1",
		ChildRunID: "child-r1",
	})
	if row1.RunID != "run-1" {
		t.Fatalf("row1.RunID = %q, want run-1", row1.RunID)
	}

	// Run 1 completes.
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "run-1",
		Agent:      "coding",
		Chain:      "root > coding",
		Depth:      1,
		CallID:     "call-1",
		ChildRunID: "child-r1",
		Outcome:    orchevents.OutcomeDone,
		Turns:      4,
	})
	if row1.State != AgentCompleted {
		t.Fatalf("row1 state = %s, want completed", row1.State)
	}

	// Run 2: provider reuses "call-1".
	s.startDelegate(&providers.ToolCall{
		ID:        "call-1",
		Arguments: map[string]any{"agent": "coding", "task": "run 2 task"},
	})
	if len(s.rows) != 2 {
		t.Fatalf("rows = %d, want 2 rows after reused call-1", len(s.rows))
	}
	row2 := s.rows[1]
	if row2 == row1 {
		t.Fatal("row2 must be a distinct row from row1")
	}

	// Anchor row2 with RunID = "run-2".
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "run-2",
		Agent:      "coding",
		Chain:      "root > coding",
		Depth:      1,
		CallID:     "call-1",
		ChildRunID: "child-r2",
	})
	if row2.RunID != "run-2" {
		t.Fatalf("row2.RunID = %q, want run-2", row2.RunID)
	}

	// Late progress from Run 1 arrives with RunID = "run-1" and Turns = 99.
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		RunID:      "run-1",
		Agent:      "coding",
		Chain:      "root > coding",
		Depth:      1,
		CallID:     "call-1",
		ChildRunID: "child-r1",
		Turns:      99,
	})

	// Progress for Run 2 arrives with RunID = "run-2" and Turns = 2.
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		RunID:      "run-2",
		Agent:      "coding",
		Chain:      "root > coding",
		Depth:      1,
		CallID:     "call-1",
		ChildRunID: "child-r2",
		Turns:      2,
	})

	// Verify row1 (Run 1) remained terminal and did not adopt turns 99.
	if row1.State != AgentCompleted || row1.Turns != 4 {
		t.Fatalf("row1 corrupted: state=%s turns=%d", row1.State, row1.Turns)
	}

	// Verify row2 (Run 2) adopted turns 2, NOT turns 99 from Run 1!
	if row2.Turns != 2 {
		t.Fatalf("row2 turns = %d, want 2 (contaminated by Run 1's turn 99)", row2.Turns)
	}
}

// Scenario 3: Cross-feed first-terminal-wins under concurrent feeds.
// When the stream feed (finishDelegate) and the structured feed (ChildCompleted)
// race, the first terminal outcome must be immutable and adopted, while
// counter snapshots take the maximum.
func TestOrchLifecycle_CrossFeedFirstTerminalWins(t *testing.T) {
	// Case A: Stream feed arrives first (Completed), followed by Structured feed (Cancelled).
	t.Run("stream_feed_first", func(t *testing.T) {
		var s agentsState
		s.startDelegate(&providers.ToolCall{
			ID:        "call-1",
			Arguments: map[string]any{"agent": "coding", "task": "subtask"},
		})
		row := s.rows[0]

		// Feed 1: EventToolFinish arrives.
		s.apply(runtime.Event{
			Type: runtime.EventToolFinish,
			ToolResult: &runtime.ToolResult{
				Name:       "delegate",
				ToolCallID: "call-1",
				Success:    true,
				Content:    "[delegate] child status=done turns=3",
			},
		})
		if row.State != AgentCompleted {
			t.Fatalf("row state = %s, want completed", row.State)
		}
		termTime := row.terminalAt
		if termTime.IsZero() {
			t.Fatal("terminalAt not stamped")
		}

		// Feed 2: Late ChildCompleted with OutcomeCancelled and higher turns arrives.
		s.applyOrchEvent(orchevents.Event{
			Type:       orchevents.ChildCompleted,
			CallID:     "call-1",
			Chain:      "root > coding",
			Depth:      1,
			ChildRunID: "child-1",
			Outcome:    orchevents.OutcomeCancelled,
			Error:      "interrupted",
			Turns:      8,
			ToolCalls:  3,
		})

		// State must remain AgentCompleted (first terminal wins).
		if row.State != AgentCompleted {
			t.Fatalf("row state overridden: %s, want completed", row.State)
		}
		// Counter must adopt maximum (8 turns, 3 tool calls).
		if row.Turns != 8 || row.ToolCalls != 3 {
			t.Fatalf("counters not adopted by max: turns=%d tools=%d", row.Turns, row.ToolCalls)
		}
		// Terminal timestamp must not be moved.
		if !row.terminalAt.Equal(termTime) {
			t.Fatal("terminalAt was overwritten")
		}
	})

	// Case B: Structured feed arrives first (Cancelled), followed by Stream feed (Finished).
	t.Run("structured_feed_first", func(t *testing.T) {
		var s agentsState
		s.startDelegate(&providers.ToolCall{
			ID:        "call-2",
			Arguments: map[string]any{"agent": "coding", "task": "subtask"},
		})
		row := s.rows[0]

		// Feed 1: Structured ChildCompleted arrives first.
		s.applyOrchEvent(orchevents.Event{
			Type:       orchevents.ChildCompleted,
			CallID:     "call-2",
			Chain:      "root > coding",
			Depth:      1,
			ChildRunID: "child-2",
			Outcome:    orchevents.OutcomeCancelled,
			Error:      "user cancelled",
			Turns:      4,
		})
		if row.State != AgentCancelled {
			t.Fatalf("row state = %s, want cancelled", row.State)
		}
		termTime := row.terminalAt

		// Feed 2: Stream EventToolFinish arrives second.
		s.apply(runtime.Event{
			Type: runtime.EventToolFinish,
			ToolResult: &runtime.ToolResult{
				Name:       "delegate",
				ToolCallID: "call-2",
				Success:    true,
				Content:    "[delegate] child status=done turns=10",
			},
		})

		// State must remain AgentCancelled (first terminal wins).
		if row.State != AgentCancelled {
			t.Fatalf("row state overridden: %s, want cancelled", row.State)
		}
		if !row.terminalAt.Equal(termTime) {
			t.Fatal("terminalAt was overwritten")
		}
	})
}

// Scenario 4: cancelOpen racing late structured events.
// stopStream calls cancelOpen to mark active rows cancelled.
// Late progress, live text, or completion events arriving afterwards must not
// resurrect the row or alter its text/status.
func TestOrchLifecycle_CancelOpenRacingLateEvents(t *testing.T) {
	var s agentsState
	s.startDelegate(&providers.ToolCall{
		ID:        "call-1",
		Arguments: map[string]any{"agent": "coding", "task": "subtask"},
	})
	row := s.rows[0]
	row.State = AgentRunning
	row.appendOutput("initial progress")

	// cancelOpen runs when turn is aborted.
	s.cancelOpen()
	if row.State != AgentCancelled {
		t.Fatalf("row state after cancelOpen = %s, want cancelled", row.State)
	}

	// 1. Late progress must NOT resurrect the row to AgentRunning.
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		CallID:     "call-1",
		Chain:      "root > coding",
		Depth:      1,
		ChildRunID: "child-1",
		Turns:      10,
	})
	if row.State != AgentCancelled {
		t.Fatalf("late progress resurrected row: state = %s", row.State)
	}

	// 2. Late text must NOT be appended to cancelled row.
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildText,
		CallID:     "call-1",
		Chain:      "root > coding",
		Depth:      1,
		ChildRunID: "child-1",
		Text:       " extra unwanted text",
	})
	if row.outputText() != "initial progress" {
		t.Fatalf("late text mutated cancelled row: %q", row.outputText())
	}

	// 3. Late completion must NOT flip state to AgentCompleted.
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		CallID:     "call-1",
		Chain:      "root > coding",
		Depth:      1,
		ChildRunID: "child-1",
		Outcome:    orchevents.OutcomeDone,
		Status:     "success",
		Turns:      12,
	})
	if row.State != AgentCancelled {
		t.Fatalf("late completed flipped state: state = %s", row.State)
	}
}

// Scenario 5: Transcript collapse dedup across resets.
// A completed child row stages a transcript summary via flushCollapse.
// A session reset (/clear or /new) wipes agentsState.
// Late events from the old child must not trigger drainCollapsed in the new session.
func TestOrchLifecycle_DrainCollapsedDedupAcrossResets(t *testing.T) {
	m, _ := newOrchTestModel(t)

	// Run 1: start child and complete it.
	next, _ := m.Update(streamEventMsg{Event: delegateStart("call-1"), gen: m.streamGen})
	m = next.(model)
	m.agents.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		CallID:     "call-1",
		Chain:      "root > coding",
		Depth:      1,
		ChildRunID: "run-1",
		Outcome:    orchevents.OutcomeDone,
		Status:     "completed successfully",
		Turns:      3,
	})

	// /clear resets the session, wiping m.agents.
	m.Clear()
	if len(m.agents.rows) != 0 {
		t.Fatalf("Clear left %d rows", len(m.agents.rows))
	}

	// drainCollapsed in the cleared session must be empty.
	if summaries := m.agents.drainCollapsed(); len(summaries) != 0 {
		t.Fatalf("drainCollapsed after Clear returned summaries: %v", summaries)
	}

	// Run 2: start fresh child and complete it.
	next, _ = m.Update(streamEventMsg{Event: delegateStart("call-2"), gen: m.streamGen})
	m = next.(model)
	m.agents.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		CallID:     "call-2",
		Chain:      "root > coding",
		Depth:      1,
		ChildRunID: "run-2",
		Outcome:    orchevents.OutcomeDone,
		Status:     "run 2 completed",
		Turns:      5,
	})

	// drainCollapsed must return ONLY Run 2's summary.
	summaries := m.agents.drainCollapsed()
	if len(summaries) != 1 {
		t.Fatalf("drainCollapsed returned %d summaries, want 1: %v", len(summaries), summaries)
	}
	if !containsString(summaries[0], "subagent-1") || !containsString(summaries[0], "completed") {
		t.Fatalf("unexpected summary: %s", summaries[0])
	}

	// Second drain returns nothing (dedup).
	if again := m.agents.drainCollapsed(); len(again) != 0 {
		t.Fatalf("second drain was not empty: %v", again)
	}
}

// Scenario 6: Bounded-queue drop accounting under mixed lifecycle/progress pressure.
// Under queue saturation, replaceable events (progress, text, tool) are dropped
// while non-droppable lifecycle events are preserved. Drop accounting remains
// strictly conserved without counter corruption.
func TestOrchLifecycle_BoundedQueueDropAccounting(t *testing.T) {
	notify, entered, release, snapshot := parkedSink()
	s := newTuiOrchSink(1, notify)

	// Wake the pump and park it so the queue remains undrained.
	s.Emit(floodProgress(0, 1))
	<-entered

	// Fill queue up to orchQueueLimit with droppable progress events.
	for j := 1; j <= orchQueueLimit; j++ {
		s.Emit(floodProgress(1, j))
	}
	if q := sinkQueueLen(s); q != orchQueueLimit {
		t.Fatalf("queue = %d, want cap %d", q, orchQueueLimit)
	}

	// Now emit a mixed sequence under maximum pressure:
	// 50 lifecycle events (must all survive).
	const lifecycleCount = 50
	for j := 1; j <= lifecycleCount; j++ {
		s.Emit(orchevents.Event{
			Type:       orchevents.AgentSpawned,
			Agent:      "coding",
			Chain:      "root > coding",
			Depth:      1,
			CallID:     fmt.Sprintf("call-life-%d", j),
			ChildRunID: fmt.Sprintf("child-life-%d", j),
		})
	}

	// 50 more droppable text events.
	const droppableCount = 50
	for j := 1; j <= droppableCount; j++ {
		s.Emit(orchevents.Event{
			Type:   orchevents.ChildText,
			CallID: "call-life-1",
			Text:   "chunk",
		})
	}

	// Release the pump, close, and join.
	release()
	s.Close()
	joinPump(t, s)

	// Verify exact conservation:
	totalEmitted := 1 + orchQueueLimit + lifecycleCount + droppableCount
	accounting(t, s, totalEmitted)

	// All 50 lifecycle events must have been delivered without hard drops.
	if s.dropsHard != 0 {
		t.Fatalf("dropsHard = %d, want 0 (lifecycle events dropped under pressure)", s.dropsHard)
	}
	if s.dropsDroppable != lifecycleCount+droppableCount {
		t.Fatalf("dropsDroppable = %d, want %d", s.dropsDroppable, lifecycleCount+droppableCount)
	}

	// Check delivered messages contain all 50 lifecycle events.
	events := drainEvents(snapshot())
	lifecycleDelivered := 0
	for _, ev := range events {
		if ev.Type == orchevents.AgentSpawned {
			lifecycleDelivered++
		}
	}
	if lifecycleDelivered != lifecycleCount {
		t.Fatalf("delivered %d AgentSpawned events, want %d", lifecycleDelivered, lifecycleCount)
	}
}

// Scenario 8: Nested-event anchoring after a reset.
// Subagent A (depth 1) in Run 1 spawns Subagent B (depth 2).
// Session reset occurs. A new delegation starts in Session 2.
// Late events from Subagent B (depth 2) with Run 1's identity arrive.
// They must not anchor to Session 2's parent or create orphan zombie rows.
func TestOrchLifecycle_NestedEventAnchoringAfterReset(t *testing.T) {
	var s agentsState

	// Session 2 parent row exists with RunID = "run-2" and Chain = "root > researcher".
	s.startDelegate(&providers.ToolCall{
		ID:        "call-s2",
		Arguments: map[string]any{"agent": "researcher", "task": "session 2 research"},
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "run-2",
		Agent:      "researcher",
		Chain:      "root > researcher",
		Depth:      1,
		CallID:     "call-s2",
		ChildRunID: "child-s2-parent",
	})
	if len(s.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(s.rows))
	}

	// Now late nested event arrives from Subagent B (depth 2) of Run 1!
	// It has Chain = "root > researcher > coder" and RunID = "run-1".
	// Its parent chain ("root > researcher") matches the string name of Session 2's parent,
	// but the RunID does NOT match ("run-1" vs "run-2").
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		RunID:      "run-1",
		Agent:      "coder",
		Chain:      "root > researcher > coder",
		Depth:      2,
		CallID:     "call-old-child",
		ChildRunID: "child-old-b",
		Turns:      5,
	})

	// Must NOT create an orphan row or attach to Session 2 parent!
	if len(s.rows) != 1 {
		t.Fatalf("rows = %d, want 1; nested straggler created an unauthorized row: %+v", len(s.rows), s.rows)
	}
	if s.byChildRun["child-old-b"] != nil {
		t.Fatal("nested straggler anchored to Session 2 parent")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestOrchLifecycle_TurnTurnAttachmentAndScopeLifecycle verifies Finding F1:
// - Turn 1 starts with sink attached; stopStream detaches the sink on turn end.
// - Turn 2 (Enter): attachOrchSink runs BEFORE stream creation, capturing the active sink.
// - Turn 3 (subsequent turn): attachOrchSink runs before stream creation again.
// - Plan-build turn: startBackgroundTurn attaches the sink before StreamChatWithMode.
// - Stream creation error: attachOrchSink is followed immediately by detachOrchSink on error.
func TestOrchLifecycle_TurnTurnAttachmentAndScopeLifecycle(t *testing.T) {
	m, _ := newOrchTestModel(t)
	rt, err := runtime.New()
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	var hookLog []bool
	orchAttachHook = func(attached bool) {
		hookLog = append(hookLog, attached)
	}
	t.Cleanup(func() { orchAttachHook = nil })

	// Turn 1 setup: installRuntime installs runtime and attaches sink
	m = m.installRuntime(runtimeReadyMsg{rt: rt})
	if len(hookLog) != 1 || !hookLog[0] {
		t.Fatalf("Turn 1 attach failed, log: %v", hookLog)
	}

	// Turn 1 completes: stopStream detaches sink
	m.stopStream(false)
	if len(hookLog) != 2 || hookLog[1] {
		t.Fatalf("Turn 1 stopStream did not detach, log: %v", hookLog)
	}

	// Turn 2: User submits via KeyEnter
	m.input.SetValue("turn 2 prompt")
	hookLog = nil
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	// attachOrchSink must have run before stream creation:
	if len(hookLog) < 1 || !hookLog[0] {
		t.Fatalf("Turn 2 did not attach sink before stream, hookLog: %v", hookLog)
	}
	// Turn 2 teardown detaches sink:
	m.stopStream(false)
	if len(hookLog) != 2 || hookLog[1] {
		t.Fatalf("Turn 2 teardown did not detach sink, hookLog: %v", hookLog)
	}

	// Turn 3: Subsequent turn after another detach/reattach
	m.input.SetValue("turn 3 prompt")
	hookLog = nil
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(hookLog) < 1 || !hookLog[0] {
		t.Fatalf("Turn 3 did not attach sink before stream, hookLog: %v", hookLog)
	}
	// Turn 3 teardown detaches sink:
	m.stopStream(false)
	if len(hookLog) != 2 || hookLog[1] {
		t.Fatalf("Turn 3 teardown did not detach sink, hookLog: %v", hookLog)
	}

	// Plan-build delegation: beginTurn attaches the sink before StreamChatWithMode
	hookLog = nil
	_ = m.beginTurn("plan task", turnPlan)
	if len(hookLog) < 1 || !hookLog[0] {
		t.Fatalf("Plan-build turn did not attach sink, hookLog: %v", hookLog)
	}
	m.stopStream(false)
	if len(hookLog) != 2 || hookLog[1] {
		t.Fatalf("Plan-build teardown did not detach sink, hookLog: %v", hookLog)
	}

	// Stream creation failure cleanup: verify detachOrchSink cleans up cleanly
	m.attachOrchSink()
	m.detachOrchSink()
	if len(hookLog) < 4 || hookLog[len(hookLog)-1] {
		t.Fatalf("detachOrchSink did not detach sink cleanly, hookLog: %v", hookLog)
	}
}

// TestOrchLifecycle_ActiveSinkCleanupOnShutdown verifies Finding F2:
// - Initial sink creation registers with holder.
// - Sink replacement after /new or /clear replaces active sink and terminates old pump.
// - Program shutdown via holder.Close() closes active sink and terminates pump goroutine.
// - Repeated reset and shutdown terminates all pump goroutines deterministically.
func TestOrchLifecycle_ActiveSinkCleanupOnShutdown(t *testing.T) {
	notify, _ := collector()
	holder := &orchSinkHolder{}

	// 1. Initial sink creation
	sink1 := newTuiOrchSink(0, notify)
	holder.Set(sink1)
	if holder.Active() != sink1 {
		t.Fatalf("holder.Active() = %v, want sink1 %v", holder.Active(), sink1)
	}

	// Model setup with holder
	m := newStartingModel(&config.Config{Orchestration: config.Orchestration{Enabled: true}}, session.New(), nil)
	m.orchEnabled = true
	m.sinkHolder = holder
	m.orchSink = sink1
	m.notify = notify

	// 2. Sink replacement after bumpOrchGen (/new or /clear)
	m.bumpOrchGen()
	sink2 := m.orchSink
	if sink2 == sink1 {
		t.Fatal("bumpOrchGen did not create new sink instance")
	}
	if holder.Active() != sink2 {
		t.Fatalf("holder.Active() = %v, want sink2 %v", holder.Active(), sink2)
	}
	// Old pump must terminate deterministically
	joinPump(t, sink1)

	// 3. Repeated reset (/clear followed by another /new)
	m.bumpOrchGen()
	sink3 := m.orchSink
	if sink3 == sink2 {
		t.Fatal("bumpOrchGen did not create third sink instance")
	}
	if holder.Active() != sink3 {
		t.Fatalf("holder.Active() = %v, want sink3 %v", holder.Active(), sink3)
	}
	// Second pump terminates
	joinPump(t, sink2)

	// 4. Program shutdown terminates active sink pump
	holder.Close()
	joinPump(t, sink3)
	if holder.Active() != nil {
		t.Fatalf("holder.Active() = %v after Close, want nil", holder.Active())
	}
}

// TestOrchLifecycle_UnanchoredCallIDCrossRunCollision verifies Finding F3:
// 1. Create a row for Run 2 with reused CallID and initially unanchored state.
// 2. Deliver late Run 1 event before Run 2 AgentSpawned.
// 3. Verify Run 1 cannot claim or mutate Run 2's row.
// 4. Deliver Run 2's legitimate AgentSpawned event.
// 5. Verify the correct row is anchored and completes normally.
// Also tests legitimate events with missing RunID for backward compatibility.
func TestOrchLifecycle_UnanchoredCallIDCrossRunCollision(t *testing.T) {
	var s agentsState

	// Run 1: provider uses call-reused.
	s.startDelegate(&providers.ToolCall{
		ID:        "call-reused",
		Arguments: map[string]any{"agent": "coder", "task": "run 1 subtask"},
	})
	row1 := s.rows[0]
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "run-1",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-reused",
		ChildRunID: "child-r1",
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "run-1",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-reused",
		ChildRunID: "child-r1",
		Outcome:    orchevents.OutcomeDone,
		Turns:      3,
	})
	if row1.State != AgentCompleted || row1.RunID != "run-1" {
		t.Fatalf("row1 state = %s, want completed", row1.State)
	}

	// 1. Create a row for Run 2 with reused CallID "call-reused" and an initially unanchored state.
	s.startDelegate(&providers.ToolCall{
		ID:        "call-reused",
		Arguments: map[string]any{"agent": "coder", "task": "run 2 subtask"},
	})
	if len(s.rows) != 2 {
		t.Fatalf("rows count = %d, want 2", len(s.rows))
	}
	row2 := s.rows[1]
	if row2.RunID != "" {
		t.Fatalf("row2.RunID = %q, want empty initially unanchored", row2.RunID)
	}

	// 2. Deliver a late Run 1 event before Run 2's AgentSpawned event.
	s.applyOrchEvent(orchevents.Event{
		Type:   orchevents.DelegationRejected,
		RunID:  "run-1",
		Agent:  "coder",
		Chain:  "root > coder",
		Depth:  1,
		CallID: "call-reused",
		Reason: "late rejection from run 1",
	})

	// 3. Verify Run 1 cannot claim or mutate Run 2's row.
	if row2.RunID != "" {
		t.Fatalf("row2 was hijacked by Run 1: RunID = %q, want empty", row2.RunID)
	}
	if row2.State != AgentQueued {
		t.Fatalf("row2 state mutated: %s, want %s", row2.State, AgentQueued)
	}

	// 4. Deliver Run 2's legitimate AgentSpawned event.
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "run-2",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-reused",
		ChildRunID: "child-r2",
	})

	// 5. Verify the correct row is anchored and completes normally.
	if row2.RunID != "run-2" {
		t.Fatalf("row2.RunID = %q, want run-2", row2.RunID)
	}
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "run-2",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-reused",
		ChildRunID: "child-r2",
		Outcome:    orchevents.OutcomeDone,
		Turns:      5,
	})
	if row2.State != AgentCompleted || row2.Turns != 5 {
		t.Fatalf("row2 not completed: state=%s turns=%d", row2.State, row2.Turns)
	}

	// Also test legitimate events with missing RunID so compatibility is explicit.
	s.startDelegate(&providers.ToolCall{
		ID:        "call-legacy",
		Arguments: map[string]any{"agent": "legacy", "task": "legacy task"},
	})
	rowLegacy := s.rows[2]
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "", // explicitly empty
		Agent:      "legacy",
		Chain:      "root > legacy",
		Depth:      1,
		CallID:     "call-legacy",
		ChildRunID: "child-legacy",
	})
	if s.byChildRun["child-legacy"] != rowLegacy {
		t.Fatal("legacy event without RunID failed to anchor to unanchored row")
	}
}

// 1. Two sibling agents with distinct CallIDs and identical RunID.
func TestF3_TwoSiblingAgentsWithDistinctCallIDsAndIdenticalRunID(t *testing.T) {
	var s agentsState
	const runID = "run-siblings-2"

	// Model spawns two sibling agents in the same execution
	s.startDelegate(&providers.ToolCall{
		ID:        "call-sib-a",
		Arguments: map[string]any{"agent": "researcher", "task": "gather requirements"},
	})
	s.startDelegate(&providers.ToolCall{
		ID:        "call-sib-b",
		Arguments: map[string]any{"agent": "coder", "task": "implement feature"},
	})

	if len(s.rows) != 2 {
		t.Fatalf("rows count = %d, want 2", len(s.rows))
	}
	rowA := s.rows[0]
	rowB := s.rows[1]

	// Structured AgentSpawned arrives for Child A
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      runID,
		Agent:      "researcher",
		Chain:      "root > researcher",
		Depth:      1,
		CallID:     "call-sib-a",
		ChildRunID: "child-r2-a",
	})
	if rowA.RunID != runID {
		t.Fatalf("rowA.RunID = %q, want %q", rowA.RunID, runID)
	}

	// Structured AgentSpawned arrives for Child B in the SAME execution
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      runID,
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-sib-b",
		ChildRunID: "child-r2-b",
	})
	// MUST NOT be rejected by hasRunID!
	if rowB.RunID != runID {
		t.Fatalf("rowB.RunID = %q, want %q (sibling child B was dropped by correlation!)", rowB.RunID, runID)
	}
	if s.byChildRun["child-r2-b"] != rowB {
		t.Fatal("child B was not indexed in byChildRun")
	}

	// Progress and completion events for Child A
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		RunID:      runID,
		CallID:     "call-sib-a",
		ChildRunID: "child-r2-a",
		Turns:      3,
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      runID,
		CallID:     "call-sib-a",
		ChildRunID: "child-r2-a",
		Outcome:    orchevents.OutcomeDone,
		Turns:      3,
	})

	// Progress and completion events for Child B
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		RunID:      runID,
		CallID:     "call-sib-b",
		ChildRunID: "child-r2-b",
		Turns:      7,
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      runID,
		CallID:     "call-sib-b",
		ChildRunID: "child-r2-b",
		Outcome:    orchevents.OutcomeDone,
		Turns:      7,
	})

	// Assert final states of both siblings
	if rowA.State != AgentCompleted || rowA.Turns != 3 {
		t.Fatalf("rowA state=%s turns=%d, want completed turns=3", rowA.State, rowA.Turns)
	}
	if rowB.State != AgentCompleted || rowB.Turns != 7 {
		t.Fatalf("rowB state=%s turns=%d, want completed turns=7", rowB.State, rowB.Turns)
	}
}

// 2. At least three sibling agents.
func TestF3_ThreeOrMoreSiblingAgents(t *testing.T) {
	var s agentsState
	const runID = "run-siblings-3"

	// Start 3 sibling delegations
	s.startDelegate(&providers.ToolCall{
		ID:        "call-3-1",
		Arguments: map[string]any{"agent": "agent1", "task": "task 1"},
	})
	s.startDelegate(&providers.ToolCall{
		ID:        "call-3-2",
		Arguments: map[string]any{"agent": "agent2", "task": "task 2"},
	})
	s.startDelegate(&providers.ToolCall{
		ID:        "call-3-3",
		Arguments: map[string]any{"agent": "agent3", "task": "task 3"},
	})

	if len(s.rows) != 3 {
		t.Fatalf("rows count = %d, want 3", len(s.rows))
	}

	// Deliver AgentSpawned for all 3
	for i := 1; i <= 3; i++ {
		callID := fmt.Sprintf("call-3-%d", i)
		childID := fmt.Sprintf("child-3-%d", i)
		chain := fmt.Sprintf("root > agent%d", i)
		s.applyOrchEvent(orchevents.Event{
			Type:       orchevents.AgentSpawned,
			RunID:      runID,
			Agent:      fmt.Sprintf("agent%d", i),
			Chain:      chain,
			Depth:      1,
			CallID:     callID,
			ChildRunID: childID,
		})
	}

	// Assert all 3 anchored to runID
	for i := 0; i < 3; i++ {
		if s.rows[i].RunID != runID {
			t.Fatalf("row %d has RunID = %q, want %q", i, s.rows[i].RunID, runID)
		}
	}

	// Sibling 1 completes successfully
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      runID,
		CallID:     "call-3-1",
		ChildRunID: "child-3-1",
		Outcome:    orchevents.OutcomeDone,
		Turns:      4,
	})
	// Sibling 2 cancelled
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      runID,
		CallID:     "call-3-2",
		ChildRunID: "child-3-2",
		Outcome:    orchevents.OutcomeCancelled,
		Turns:      2,
	})
	// Sibling 3 failed
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      runID,
		CallID:     "call-3-3",
		ChildRunID: "child-3-3",
		Outcome:    orchevents.OutcomeError,
		Error:      "compilation failed",
		Turns:      6,
	})

	if s.rows[0].State != AgentCompleted || s.rows[0].Turns != 4 {
		t.Fatalf("row 0 state=%s turns=%d, want completed turns=4", s.rows[0].State, s.rows[0].Turns)
	}
	if s.rows[1].State != AgentCancelled || s.rows[1].Turns != 2 {
		t.Fatalf("row 1 state=%s turns=%d, want cancelled turns=2", s.rows[1].State, s.rows[1].Turns)
	}
	if s.rows[2].State != AgentFailed || s.rows[2].Turns != 6 {
		t.Fatalf("row 2 state=%s turns=%d, want failed turns=6", s.rows[2].State, s.rows[2].Turns)
	}
}

// 3. Same CallID reused across two executions.
func TestF3_SameCallIDReusedAcrossTwoExecutions(t *testing.T) {
	var s agentsState

	// Execution 1
	s.startDelegate(&providers.ToolCall{
		ID:        "call-reused-key",
		Arguments: map[string]any{"agent": "coder", "task": "run 1 task"},
	})
	row1 := s.rows[0]
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "run-exec-1",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-reused-key",
		ChildRunID: "child-exec-1",
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "run-exec-1",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-reused-key",
		ChildRunID: "child-exec-1",
		Outcome:    orchevents.OutcomeDone,
		Turns:      3,
	})
	if row1.State != AgentCompleted || row1.RunID != "run-exec-1" {
		t.Fatalf("row1 not completed: state=%s runID=%q", row1.State, row1.RunID)
	}

	// Execution 2 reuses the same tool call ID
	s.startDelegate(&providers.ToolCall{
		ID:        "call-reused-key",
		Arguments: map[string]any{"agent": "coder", "task": "run 2 task"},
	})
	if len(s.rows) != 2 {
		t.Fatalf("rows count = %d, want 2", len(s.rows))
	}
	row2 := s.rows[1]
	if row2 == row1 {
		t.Fatal("row2 must be a separate row instance")
	}
	if row2.RunID != "" {
		t.Fatalf("row2.RunID = %q, want empty initially unanchored", row2.RunID)
	}

	// Late event from Execution 1 arrives with Turns = 99
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		RunID:      "run-exec-1",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-reused-key",
		ChildRunID: "child-exec-1",
		Turns:      99,
	})

	// row2 must remain untouched
	if row2.RunID != "" || row2.State != AgentQueued {
		t.Fatalf("row2 was contaminated: RunID=%q state=%s", row2.RunID, row2.State)
	}

	// Execution 2 sends AgentSpawned
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "run-exec-2",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-reused-key",
		ChildRunID: "child-exec-2",
	})
	if row2.RunID != "run-exec-2" {
		t.Fatalf("row2.RunID = %q, want run-exec-2", row2.RunID)
	}

	// Execution 2 completes
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "run-exec-2",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-reused-key",
		ChildRunID: "child-exec-2",
		Outcome:    orchevents.OutcomeDone,
		Turns:      5,
	})

	if row1.Turns != 3 {
		t.Fatalf("row1 turns modified: %d, want 3", row1.Turns)
	}
	if row2.Turns != 5 || row2.State != AgentCompleted {
		t.Fatalf("row2 state=%s turns=%d, want completed turns=5", row2.State, row2.Turns)
	}
}

// 4. Late old-run event before new-run AgentSpawned.
func TestF3_LateOldRunEventBeforeNewRunAgentSpawned(t *testing.T) {
	var s agentsState

	// Run 1: spawns child A with call-alpha
	s.startDelegate(&providers.ToolCall{
		ID:        "call-alpha",
		Arguments: map[string]any{"agent": "reviewer", "task": "run 1 review"},
	})
	rowA := s.rows[0]
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "old-run-4",
		Agent:      "reviewer",
		Chain:      "root > reviewer",
		Depth:      1,
		CallID:     "call-alpha",
		ChildRunID: "child-alpha-1",
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "old-run-4",
		Agent:      "reviewer",
		Chain:      "root > reviewer",
		Depth:      1,
		CallID:     "call-alpha",
		ChildRunID: "child-alpha-1",
		Outcome:    orchevents.OutcomeDone,
		Turns:      2,
	})

	// Run 2: spawns child B with same call-alpha
	s.startDelegate(&providers.ToolCall{
		ID:        "call-alpha",
		Arguments: map[string]any{"agent": "reviewer", "task": "run 2 review"},
	})
	rowB := s.rows[1]
	if rowB.RunID != "" {
		t.Fatalf("rowB.RunID = %q, want empty", rowB.RunID)
	}

	// Late event from Run 1 arrives BEFORE Run 2's AgentSpawned:
	s.applyOrchEvent(orchevents.Event{
		Type:   orchevents.DelegationRejected,
		RunID:  "old-run-4",
		Agent:  "reviewer",
		Chain:  "root > reviewer",
		Depth:  1,
		CallID: "call-alpha",
		Reason: "late rejection from run 1",
	})

	// rowB must NOT be claimed or rejected by Run 1's event
	if rowB.RunID != "" || rowB.State != AgentQueued {
		t.Fatalf("rowB was hijacked by late old-run rejection: RunID=%q state=%s", rowB.RunID, rowB.State)
	}
	// rowA was completed and must not be flipped to rejected
	if rowA.State != AgentCompleted {
		t.Fatalf("rowA state flipped to %s, want completed", rowA.State)
	}

	// Now Run 2 sends legitimate AgentSpawned
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "new-run-4",
		Agent:      "reviewer",
		Chain:      "root > reviewer",
		Depth:      1,
		CallID:     "call-alpha",
		ChildRunID: "child-alpha-2",
	})
	if rowB.RunID != "new-run-4" {
		t.Fatalf("rowB.RunID = %q, want new-run-4", rowB.RunID)
	}

	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "new-run-4",
		Agent:      "reviewer",
		Chain:      "root > reviewer",
		Depth:      1,
		CallID:     "call-alpha",
		ChildRunID: "child-alpha-2",
		Outcome:    orchevents.OutcomeDone,
		Turns:      4,
	})
	if rowB.State != AgentCompleted || rowB.Turns != 4 {
		t.Fatalf("rowB state=%s turns=%d, want completed turns=4", rowB.State, rowB.Turns)
	}
}

// 5. Nested child events retain correct parent/chain correlation.
func TestF3_NestedChildEvents(t *testing.T) {
	var s agentsState
	const runID = "run-nested-5"

	// Depth 1 parent
	s.startDelegate(&providers.ToolCall{
		ID:        "call-mgr",
		Arguments: map[string]any{"agent": "manager", "task": "manage project"},
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      runID,
		Agent:      "manager",
		Chain:      "root > manager",
		Depth:      1,
		CallID:     "call-mgr",
		ChildRunID: "child-mgr-1",
	})
	if len(s.rows) != 1 {
		t.Fatalf("rows count = %d, want 1", len(s.rows))
	}

	// Depth 2 child arrives with matching parent chain
	s.applyOrchEvent(orchevents.Event{
		Type:   orchevents.DelegationRequested,
		RunID:  runID,
		Agent:  "worker",
		Chain:  "root > manager > worker",
		Depth:  2,
		CallID: "call-wrk",
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      runID,
		Agent:      "worker",
		Chain:      "root > manager > worker",
		Depth:      2,
		CallID:     "call-wrk",
		ChildRunID: "child-wrk-1",
	})
	if len(s.rows) != 2 {
		t.Fatalf("nested child was not created: rows count = %d, want 2", len(s.rows))
	}
	rowWorker := s.rows[1]
	if rowWorker.ParentLabel != "subagent-1" {
		t.Fatalf("worker parent = %q, want subagent-1", rowWorker.ParentLabel)
	}

	// Mismatched RunID for depth 2 must NOT attach or create row
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		RunID:      "wrong-run-id",
		Agent:      "intruder",
		Chain:      "root > manager > intruder",
		Depth:      2,
		CallID:     "call-intruder",
		ChildRunID: "child-intruder",
		Turns:      1,
	})
	if len(s.rows) != 2 {
		t.Fatalf("intruder created an unauthorized nested row: rows=%d, want 2", len(s.rows))
	}

	// Legitimate worker progress and completion
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		RunID:      runID,
		CallID:     "call-wrk",
		ChildRunID: "child-wrk-1",
		Turns:      5,
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      runID,
		CallID:     "call-wrk",
		ChildRunID: "child-wrk-1",
		Outcome:    orchevents.OutcomeDone,
		Turns:      5,
	})
	if rowWorker.State != AgentCompleted || rowWorker.Turns != 5 {
		t.Fatalf("worker state=%s turns=%d, want completed turns=5", rowWorker.State, rowWorker.Turns)
	}
}

// 6. Legacy empty-RunID behavior.
func TestF3_LegacyEmptyRunIDBehavior(t *testing.T) {
	var s agentsState

	s.startDelegate(&providers.ToolCall{
		ID:        "call-legacy-flow",
		Arguments: map[string]any{"agent": "coder", "task": "legacy work"},
	})
	row := s.rows[0]

	// Structured events with empty RunID
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-legacy-flow",
		ChildRunID: "child-legacy-flow-1",
	})
	if s.byChildRun["child-legacy-flow-1"] != row {
		t.Fatal("legacy AgentSpawned failed to anchor row")
	}

	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildProgress,
		RunID:      "",
		CallID:     "call-legacy-flow",
		ChildRunID: "child-legacy-flow-1",
		Turns:      3,
	})
	if row.Turns != 3 {
		t.Fatalf("row turns = %d, want 3", row.Turns)
	}

	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "",
		CallID:     "call-legacy-flow",
		ChildRunID: "child-legacy-flow-1",
		Outcome:    orchevents.OutcomeDone,
		Turns:      3,
	})
	if row.State != AgentCompleted {
		t.Fatalf("row state = %s, want completed", row.State)
	}
}

// 7. First-terminal-wins after correlation fix.
func TestF3_FirstTerminalWinsAfterCorrelation(t *testing.T) {
	var s agentsState
	const runID = "run-ftw-7"

	s.startDelegate(&providers.ToolCall{
		ID:        "call-ftw-a",
		Arguments: map[string]any{"agent": "agentA", "task": "task A"},
	})
	s.startDelegate(&providers.ToolCall{
		ID:        "call-ftw-b",
		Arguments: map[string]any{"agent": "agentB", "task": "task B"},
	})
	rowA := s.rows[0]
	rowB := s.rows[1]

	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      runID,
		Agent:      "agentA",
		Chain:      "root > agentA",
		Depth:      1,
		CallID:     "call-ftw-a",
		ChildRunID: "child-a",
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      runID,
		Agent:      "agentB",
		Chain:      "root > agentB",
		Depth:      1,
		CallID:     "call-ftw-b",
		ChildRunID: "child-b",
	})

	// Row A: Stream feed finishes first (completed), then late structured cancelled arrives
	s.apply(runtime.Event{
		Type: runtime.EventToolFinish,
		ToolResult: &runtime.ToolResult{
			Name:       "delegate",
			ToolCallID: "call-ftw-a",
			Success:    true,
			Content:    "[delegate] child status=done turns=2",
		},
	})
	if rowA.State != AgentCompleted {
		t.Fatalf("rowA state = %s, want completed", rowA.State)
	}

	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      runID,
		CallID:     "call-ftw-a",
		ChildRunID: "child-a",
		Outcome:    orchevents.OutcomeCancelled,
		Turns:      5,
	})
	if rowA.State != AgentCompleted {
		t.Fatalf("rowA state overridden: %s, want completed", rowA.State)
	}
	if rowA.Turns != 5 {
		t.Fatalf("rowA turns not adopted by max: got %d, want 5", rowA.Turns)
	}

	// Row B: Structured feed finishes first (cancelled), then stream feed arrives
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      runID,
		CallID:     "call-ftw-b",
		ChildRunID: "child-b",
		Outcome:    orchevents.OutcomeCancelled,
		Turns:      4,
	})
	if rowB.State != AgentCancelled {
		t.Fatalf("rowB state = %s, want cancelled", rowB.State)
	}

	s.apply(runtime.Event{
		Type: runtime.EventToolFinish,
		ToolResult: &runtime.ToolResult{
			Name:       "delegate",
			ToolCallID: "call-ftw-b",
			Success:    true,
			Content:    "[delegate] child status=done turns=8",
		},
	})
	if rowB.State != AgentCancelled {
		t.Fatalf("rowB state overridden: %s, want cancelled", rowB.State)
	}
}

// TestIntegration_F3_F4_Interaction validates end-to-end integration:
// 1. Multiple siblings share one execution ID but have distinct CallIDs.
// 2. Reused CallIDs across executions remain isolated.
// 3. A delayed old-run event cannot hijack a new row.
// 4. Nested children remain correlated after parent completion or cancellation.
// 5. Runtime reuse cannot leak execution identity into a later TUI or headless run.
func TestIntegration_F3_F4_Interaction(t *testing.T) {
	var s agentsState

	// Run 1: Top-level execution "exec-run-1" (e.g. established via runtime.WithExecutionID)
	const exec1 = "exec-run-1"

	// 1. Multiple siblings share one execution ID but have distinct CallIDs
	s.startDelegate(&providers.ToolCall{
		ID:        "call-shared-1",
		Arguments: map[string]any{"agent": "parent-agent", "task": "manage multi-step"},
	})
	s.startDelegate(&providers.ToolCall{
		ID:        "call-shared-2",
		Arguments: map[string]any{"agent": "coder", "task": "code feature"},
	})

	if len(s.rows) != 2 {
		t.Fatalf("run 1: rows = %d, want 2", len(s.rows))
	}
	row1 := s.rows[0]
	row2 := s.rows[1]

	// Structured AgentSpawned arrives for both siblings under exec1
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      exec1,
		Agent:      "parent-agent",
		Chain:      "root > parent-agent",
		Depth:      1,
		CallID:     "call-shared-1",
		ChildRunID: "child-r1-1",
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      exec1,
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-shared-2",
		ChildRunID: "child-r1-2",
	})

	// Both siblings must anchor to exec1
	if row1.RunID != exec1 || row2.RunID != exec1 {
		t.Fatalf("run 1 siblings failed to anchor: row1=%q row2=%q", row1.RunID, row2.RunID)
	}

	// 4. Nested child spawned by parent-agent
	s.applyOrchEvent(orchevents.Event{
		Type:   orchevents.DelegationRequested,
		RunID:  exec1,
		Agent:  "sub-worker",
		Chain:  "root > parent-agent > sub-worker",
		Depth:  2,
		CallID: "call-nested-w",
	})
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      exec1,
		Agent:      "sub-worker",
		Chain:      "root > parent-agent > sub-worker",
		Depth:      2,
		CallID:     "call-nested-w",
		ChildRunID: "child-r1-nested",
	})
	if len(s.rows) != 3 {
		t.Fatalf("nested row count = %d, want 3", len(s.rows))
	}
	nestedRow := s.rows[2]
	if nestedRow.ParentLabel != "subagent-1" {
		t.Fatalf("nested row parent = %q, want subagent-1", nestedRow.ParentLabel)
	}

	// Parent completes first; nested child must remain correlated and complete after parent
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      exec1,
		CallID:     "call-shared-1",
		ChildRunID: "child-r1-1",
		Outcome:    orchevents.OutcomeDone,
		Turns:      3,
	})
	if row1.State != AgentCompleted {
		t.Fatalf("parent row state = %s, want completed", row1.State)
	}

	// Nested child now completes
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      exec1,
		CallID:     "call-nested-w",
		ChildRunID: "child-r1-nested",
		Outcome:    orchevents.OutcomeDone,
		Turns:      6,
	})
	if nestedRow.State != AgentCompleted || nestedRow.Turns != 6 {
		t.Fatalf("nested row state=%s turns=%d, want completed turns=6", nestedRow.State, nestedRow.Turns)
	}

	// Sibling 2 completes
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      exec1,
		CallID:     "call-shared-2",
		ChildRunID: "child-r1-2",
		Outcome:    orchevents.OutcomeDone,
		Turns:      4,
	})

	// 2. Run 2 starts: Reuses call-shared-1
	const exec2 = "exec-run-2"
	s.startDelegate(&providers.ToolCall{
		ID:        "call-shared-1",
		Arguments: map[string]any{"agent": "parent-agent", "task": "run 2 task"},
	})
	if len(s.rows) != 4 {
		t.Fatalf("run 2: rows count = %d, want 4", len(s.rows))
	}
	rowRun2 := s.rows[3]
	if rowRun2.RunID != "" {
		t.Fatalf("new row must be unanchored, got %q", rowRun2.RunID)
	}

	// 3. Delayed old-run event from exec1 arrives BEFORE new-run AgentSpawned
	s.applyOrchEvent(orchevents.Event{
		Type:   orchevents.DelegationRejected,
		RunID:  exec1,
		Agent:  "parent-agent",
		Chain:  "root > parent-agent",
		Depth:  1,
		CallID: "call-shared-1",
		Reason: "stale reject from exec 1",
	})

	// rowRun2 must NOT be claimed or mutated by the delayed old-run event
	if rowRun2.RunID != "" || rowRun2.State != AgentQueued {
		t.Fatalf("rowRun2 hijacked by delayed exec1 event: RunID=%q state=%s", rowRun2.RunID, rowRun2.State)
	}
	// row1 must remain AgentCompleted
	if row1.State != AgentCompleted {
		t.Fatalf("row1 mutated: state=%s, want completed", row1.State)
	}

	// Legitimate Run 2 AgentSpawned arrives with exec2
	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      exec2,
		Agent:      "parent-agent",
		Chain:      "root > parent-agent",
		Depth:      1,
		CallID:     "call-shared-1",
		ChildRunID: "child-r2-1",
	})
	if rowRun2.RunID != exec2 {
		t.Fatalf("rowRun2 failed to anchor to exec2: %q", rowRun2.RunID)
	}

	s.applyOrchEvent(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      exec2,
		CallID:     "call-shared-1",
		ChildRunID: "child-r2-1",
		Outcome:    orchevents.OutcomeDone,
		Turns:      2,
	})
	if rowRun2.State != AgentCompleted || rowRun2.Turns != 2 {
		t.Fatalf("rowRun2 state=%s turns=%d, want completed turns=2", rowRun2.State, rowRun2.Turns)
	}
}
