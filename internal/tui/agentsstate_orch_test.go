package tui

// Phase 3 battery for the structured orchestration event path
// (applyOrchEvent) and the quadrant slot manager. Conventions follow
// agentsstate_terminal_test.go: real unexported API, no sleeps, direct
// state assertions.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"forcefield/internal/orchevents"
	"forcefield/internal/runtime"
)

// --- helpers -------------------------------------------------------------------------

// orchLease is a delegating scope as the runtime emits Delegation*
// events: Depth is the delegating scope's depth (child = ev.Depth+1),
// CallID is the delegate call id, ChildRunID still empty.
func orchLease(t orchevents.Type, callID, chain, agent string, depth, taskChars int) orchevents.Event {
	return orchevents.Event{
		Type:      t,
		Agent:     agent,
		Chain:     chain,
		Depth:     depth,
		CallID:    callID,
		TaskChars: taskChars,
	}
}

// orchChild is a child-scoped event as the runtime emits spawn,
// progress, completion and budget records: Depth is the child's own
// depth and the call id is inherited from the delegate call.
func orchChild(t orchevents.Type, callID, chain, agent, childRunID string, depth, turns int) orchevents.Event {
	return orchevents.Event{
		Type:       t,
		Agent:      agent,
		Chain:      chain,
		Depth:      depth,
		ChildRunID: childRunID,
		CallID:     callID,
		Turns:      turns,
	}
}

func orchToolEvent(t orchevents.Type, callID, chain, tool, childRunID string, depth int) orchevents.Event {
	return orchevents.Event{
		Type:       t,
		Chain:      chain,
		Depth:      depth,
		ChildRunID: childRunID,
		CallID:     callID,
		Tool:       tool,
	}
}

// delegateStart returns the runtime feed's delegate call start.
func delegateStart(callID string) runtime.Event {
	ev := startCall(callID)
	ev.Type = runtime.EventToolStart
	return ev
}

// delegateFinish returns a successful runtime delegate result.
func delegateFinish(callID, content string) runtime.Event {
	return runtime.Event{
		Type: runtime.EventToolFinish,
		ToolResult: &runtime.ToolResult{
			ToolCallID: callID,
			Name:       "delegate",
			Content:    content,
			Success:    true,
		},
	}
}

// delegateTurn returns the runtime feed's throttled progress line.
func delegateTurn(callID string, n int) runtime.Event {
	return runtime.Event{
		Type: runtime.EventToolProgress,
		ToolProgress: &runtime.ToolProgress{
			ToolCallID: callID,
			Stream:     "progress",
			Data:       fmt.Sprintf("[delegate: agent %q turn %d]", "coding", n),
		},
	}
}

// finishedContent mimics the delegate tool's report header.
const finishedContent = "[delegate] agent=\"coding\" turns=7 tool_calls=3 failed_tool_calls=0 denied=0\n" +
	"[delegate] child status=partial verification=verified\n\nbody"

// testClock returns the state's injected clock plus an advance func.
func testClock() (*agentsState, func(time.Duration)) {
	cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := &agentsState{now: func() time.Time { return cur }}
	return s, func(d time.Duration) { cur = cur.Add(d) }
}

// slotRow appends a raw row for slot-policy tests (state and StartedAt
// are test-controlled; the slot manager claims them like any other).
func slotRow(s *agentsState, state AgentRowState, StartedAt time.Time) *childRow {
	s.nextLabel++
	row := &childRow{
		Label:      "subagent-" + fmt.Sprint(s.nextLabel),
		State:      state,
		StartedAt:  StartedAt,
		TurnsExact: true,
		seq:        s.nextRow(),
	}
	if state.terminal() {
		row.terminalAt = StartedAt
		row.EndedAt = StartedAt
	}
	s.rows = append(s.rows, row)
	return row
}

func assertRows(t *testing.T, s *agentsState, want int) {
	t.Helper()
	if len(s.rows) != want {
		t.Fatalf("rows = %d, want %d (labels %v)", len(s.rows), want, labels(s.rows))
	}
}

func labels(rows []*childRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Label
	}
	return out
}

// --- correlation and enrichment ------------------------------------------------------

func TestOrchDelegationAnchorsRuntimeRow(t *testing.T) {
	s := &agentsState{}
	s.apply(delegateStart("call-1"))
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 412))
	s.applyOrchEvent(orchLease(orchevents.DelegationAccepted, "call-1", "general > coding", "coding", 0, 412))

	assertRows(t, s, 1)
	row := s.byCall["call-1"]
	if row == nil {
		t.Fatal("row missing for call-1")
	}
	if row.Depth != 1 {
		t.Fatalf("Depth = %d, want 1 (parent scope depth 0 + 1)", row.Depth)
	}
	if row.Chain != "general > coding" {
		t.Fatalf("Chain = %q, want %q", row.Chain, "general > coding")
	}
	if row.ParentLabel != mainAgentLabel {
		t.Fatalf("ParentLabel = %q, want %q", row.ParentLabel, mainAgentLabel)
	}
	if row.TaskChars != 412 {
		t.Fatalf("TaskChars = %d, want 412", row.TaskChars)
	}
	if row.Task != "task" {
		t.Fatalf("Task = %q, want the runtime feed's own task text, not an orch rebuild", row.Task)
	}
	if row.State != AgentQueued {
		t.Fatalf("State = %s, want queued before spawn/progress", row.State)
	}
	if s.byOrchChain["general > coding"] != row {
		t.Fatal("chain index not registered")
	}
}

func TestOrchChildEventsAnchorRunAndTrackTurns(t *testing.T) {
	s := &agentsState{}
	s.apply(delegateStart("call-1"))
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 20))

	spawn := orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0)
	s.applyOrchEvent(spawn)
	if s.byChildRun["run-1"] != s.byCall["call-1"] {
		t.Fatal("child run id not bound to the delegate call row")
	}
	if s.byCall["call-1"].State != AgentQueued {
		t.Fatalf("State = %s after spawn, want queued (no proven turn yet)", s.byCall["call-1"].State)
	}

	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 1))
	if got := s.byChildRun["run-1"]; got.State != AgentRunning {
		t.Fatalf("State = %s, want running after progress", got.State)
	}
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 3))
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 2))
	if got := s.byChildRun["run-1"]; got.Turns != 3 || !got.TurnsExact {
		t.Fatalf("Turns = %d exact=%v, want monotonic exact 3", got.Turns, got.TurnsExact)
	}
}

func TestOrchChildCompletedOutcomeMapping(t *testing.T) {
	cases := []struct {
		name    string
		outcome string
		status  string
		angle   string
		want    AgentRowState
	}{
		{"done", orchevents.OutcomeDone, "partial", "", AgentCompleted},
		{"error", orchevents.OutcomeError, "", "boom", AgentFailed},
		{"blocked", orchevents.OutcomeBlocked, "", "limit", AgentFailed},
		{"cancelled", orchevents.OutcomeCancelled, "", "stopped", AgentCancelled},
		{"unknown", "mystery", "", "", AgentFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &agentsState{}
			s.apply(delegateStart("call-1"))
			spawn := orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0)
			s.applyOrchEvent(spawn)
			done := orchChild(orchevents.ChildCompleted, "call-1", "general > coding", "coding", "run-1", 1, 5)
			done.Outcome = tc.outcome
			done.Status = tc.status
			done.Error = tc.angle
			done.ToolCalls = 3
			done.Denied = 1
			done.Failed = 2
			s.applyOrchEvent(done)

			row := s.byChildRun["run-1"]
			if row.State != tc.want {
				t.Fatalf("State = %s, want %s", row.State, tc.want)
			}
			if row.Turns != 5 || row.ToolCalls != 3 || row.DeniedCount != 1 || row.FailedCount != 2 {
				t.Fatalf("counters turns=%d tools=%d denied=%d failed=%d", row.Turns, row.ToolCalls, row.DeniedCount, row.FailedCount)
			}
			if row.EndedAt.IsZero() || row.terminalAt.IsZero() {
				t.Fatal("terminal timestamps missing")
			}
			if tc.status != "" && row.Status != tc.status {
				t.Fatalf("Status = %q, want %q", row.Status, tc.status)
			}
			if tc.angle != "" && row.Detail == "" {
				t.Fatalf("Detail empty, want scrubbed error %q", tc.angle)
			}
			if row.State == AgentFailed && row.Detail == "" {
				t.Fatal("failed row without any detail")
			}
		})
	}
}

func TestOrchFirstTerminalWinsAcrossFeeds(t *testing.T) {
	t.Run("runtime feed wins first", func(t *testing.T) {
		s := &agentsState{}
		s.apply(delegateStart("call-1"))
		s.apply(delegateTurn("call-1", 2))
		s.apply(delegateFinish("call-1", finishedContent))
		row := s.byCall["call-1"]
		if row.State != AgentCompleted || row.Status == "" {
			t.Fatalf("runtime feed did not complete the row: %s %q", row.State, row.Status)
		}
		status, detail := row.Status, row.Detail

		// A stale structured completion must not reverse or overwrite,
		// but its exact counters still adopt upward.
		done := orchChild(orchevents.ChildCompleted, "call-1", "general > coding", "coding", "run-1", 1, 9)
		done.Outcome = orchevents.OutcomeError
		done.Error = "stale"
		s.applyOrchEvent(done)
		if row.State != AgentCompleted || row.Status != status {
			t.Fatalf("late structured completion changed state: %s %q", row.State, row.Status)
		}
		if row.Detail != detail {
			t.Fatalf("Detail changed to %q", row.Detail)
		}
		if row.Turns != 9 {
			t.Fatalf("Turns = %d, want adopted exact 9", row.Turns)
		}
	})

	t.Run("structured feed wins first", func(t *testing.T) {
		s := &agentsState{}
		s.apply(delegateStart("call-1"))
		done := orchChild(orchevents.ChildCompleted, "call-1", "general > coding", "coding", "run-1", 1, 4)
		done.Outcome = orchevents.OutcomeDone
		done.Status = "partial"
		s.applyOrchEvent(done)
		row := s.byCall["call-1"]
		if row.State != AgentCompleted {
			t.Fatalf("State = %s, want structured completion", row.State)
		}
		status := row.Status

		// The runtime result arrives after (both feeds race through
		// Update): the first terminal wins, its data stays.
		s.apply(delegateFinish("call-1", finishedContent))
		if row.State != AgentCompleted || row.Status != status {
			t.Fatalf("late runtime finish reversed structured terminal: %s %q", row.State, row.Status)
		}
	})
}

func TestOrchNestedChainCreationAndLineage(t *testing.T) {
	s, advance := testClock()
	s.apply(delegateStart("call-1"))
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 20))
	parent := s.byCall["call-1"]
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0))
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 1))
	advance(10 * time.Millisecond)

	// Depth-2 delegation emitted from the child's own scope: depth 1
	// means the new child sits at depth 2.
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-2", "general > coding > verifier", "verifier", 1, 300))
	assertRows(t, s, 2)
	nested := s.byOrchCall[orchCallKey("call-2", "general > coding > verifier")]
	if nested == nil {
		t.Fatal("nested row not created (or not registered by its delegation pair)")
	}
	if nested.Label != "subagent-2" {
		t.Fatalf("Label = %q, want the shared next label", nested.Label)
	}
	if nested.Depth != 2 {
		t.Fatalf("Depth = %d, want 2", nested.Depth)
	}
	if nested.ParentLabel != parent.Label {
		t.Fatalf("ParentLabel = %q, want %q", nested.ParentLabel, parent.Label)
	}
	if got := nested.lineageLine(); got != "↳ via subagent-1 (depth 2)" {
		t.Fatalf("lineageLine = %q", got)
	}
	if parent.lineageLine() != "" {
		t.Fatalf("depth-1 lineage should be empty, got %q", parent.lineageLine())
	}

	// Spawn and progress bind the nested run to its row.
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-2", "general > coding > verifier", "verifier", "run-2", 2, 0))
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-2", "general > coding > verifier", "verifier", "run-2", 2, 1))
	if s.byChildRun["run-2"] != nested {
		t.Fatal("nested run id not bound")
	}
	if nested.State != AgentRunning {
		t.Fatalf("nested State = %s, want running", nested.State)
	}

	// Depth-3 nested under the verifier.
	advance(10 * time.Millisecond)
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-3", "general > coding > verifier > auditor", "auditor", 2, 50))
	assertRows(t, s, 3)
	deep := s.byOrchCall[orchCallKey("call-3", "general > coding > verifier > auditor")]
	if deep.Label != "subagent-3" || deep.Depth != 3 || deep.ParentLabel != nested.Label {
		t.Fatalf("depth-3 row wrong: %s depth %d parent %q", deep.Label, deep.Depth, deep.ParentLabel)
	}

	// Parent wait state is derived from live descendants, not stored.
	if w := s.nestedActiveChild(parent); w == nil || w.Label != nested.Label {
		t.Fatalf("nestedActiveChild(parent) = %v, want %s", w, nested.Label)
	}
	if w := s.nestedActiveChild(nested); w == nil || w.Label != deep.Label {
		t.Fatalf("nestedActiveChild(nested) = %v, want %s", w, deep.Label)
	}
	// Completing the subtree clears the waiting evidence.
	s.applyOrchEvent(orchChild(orchevents.ChildCompleted, "call-2", "general > coding > verifier", "verifier", "run-2", 2, 2))
	if s.nestedActiveChild(parent) == nil || s.nestedActiveChild(parent).Label != deep.Label {
		t.Fatal("ancestor wait state must survive on remaining active descendants")
	}
	if w := s.nestedActiveChild(nested); w != nil {
		t.Fatal("verifier still shows wait state after its child completed")
	}
}

func TestOrchSelfHealsWhenDelegationWasSkipped(t *testing.T) {
	s := &agentsState{}
	s.apply(delegateStart("call-1"))
	// The nested delegation's parent chain is not registered yet (the
	// parent's own delegation events were skipped earlier and the
	// nested event arrived first): skip it.
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-2", "general > coding > verifier", "verifier", "run-2", 2, 1))
	assertRows(t, s, 1)

	// The parent anchors now (its runtime row already existed); the
	// chain index registers, and the same child's next progress event
	// creates its row.
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 20))
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-2", "general > coding > verifier", "verifier", "run-2", 2, 1))
	assertRows(t, s, 2)
	nested := s.byOrchCall[orchCallKey("call-2", "general > coding > verifier")]
	if nested == nil || nested.ParentLabel != "subagent-1" {
		t.Fatalf("nested row after parent known: %+v", nested)
	}
	if nested.State != AgentRunning {
		t.Fatalf("nested State = %s, want running from progress", nested.State)
	}
}

func TestOrchFirstLevelDownstreamAnchorsWithoutDelegationEvents(t *testing.T) {
	s := &agentsState{}
	// Progress for a call the runtime feed has not started yet: skip.
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-9", "general > cyber", "cyber", "run-9", 1, 2))
	assertRows(t, s, 0)

	// The row appears, and a later progress for the same call id
	// anchors onto it (delegate call id == orch call id).
	s.apply(delegateStart("call-9"))
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-9", "general > cyber", "cyber", "run-9", 1, 4))
	row := s.byCall["call-9"]
	assertRows(t, s, 1)
	if row.State != AgentRunning || row.Turns != 4 {
		t.Fatalf("row after late anchor: state=%s turns=%d", row.State, row.Turns)
	}
	if row.Depth != 1 || row.Chain != "general > cyber" || row.ParentLabel != mainAgentLabel {
		t.Fatalf("anchored row taxonomy wrong: depth=%d chain=%q parent=%q", row.Depth, row.Chain, row.ParentLabel)
	}
}

func TestOrchNothingCreatedWithoutAnAnchor(t *testing.T) {
	s := &agentsState{}
	// Empty identity events, unknown ids, and spawn-only records:
	// none may fabricate a visible agent.
	s.applyOrchEvent(orchevents.Event{Type: orchevents.ChildCompleted, Outcome: orchevents.OutcomeDone})
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "", "general > coding", "coding", "", 1, 0))
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-x", "general > other", "other", 0, 5))
	s.applyOrchEvent(orchLease(orchevents.DelegationRejected, "call-y", "general > other", "other", 0, 9000))
	if len(s.rows) != 0 {
		t.Fatalf("phantom rows created: %v", labels(s.rows))
	}
}

func TestOrchDuplicateEventsCollapseToOne(t *testing.T) {
	s := &agentsState{}
	s.apply(delegateStart("call-1"))
	for i := 0; i < 3; i++ {
		s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 100))
		s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0))
		s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1b", 1, 0))
		s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 2))
		first := orchChild(orchevents.ChildCompleted, "call-1", "general > coding", "coding", "run-1", 1, 5)
		first.Outcome = orchevents.OutcomeDone
		first.Status = "partial"
		s.applyOrchEvent(first)
		stale := orchChild(orchevents.ChildCompleted, "call-1", "general > coding", "coding", "run-1", 1, 7)
		stale.Outcome = orchevents.OutcomeError
		stale.Error = "duplicate terminal must not reverse first-known outcome"
		s.applyOrchEvent(stale)
	}
	assertRows(t, s, 1)
	row := s.rows[0]
	if row.State != AgentCompleted {
		t.Fatalf("State = %s, want exactly one terminal transition", row.State)
	}
	if row.Turns != 7 {
		t.Fatalf("Turns = %d, want max 7", row.Turns)
	}
	// Every emitted run id correlates to the same row: no duplicates.
	if s.byChildRun["run-1"] != row || s.byChildRun["run-1b"] != row {
		t.Fatal("child run indices disagree")
	}
}

func TestOrchChainCasingNormalization(t *testing.T) {
	s := &agentsState{}
	s.apply(delegateStart("call-1"))
	// Delegation events lowercase the target; spawn events carry the
	// registry name. Both must land on one row.
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 1))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "GENERAL > CODING", "CODING", "run-1", 1, 0))
	if s.byChildRun["run-1"] != s.byCall["call-1"] {
		t.Fatal("casing mismatch split the row identity")
	}
}

func TestOrchDelegationRejectionPaths(t *testing.T) {
	t.Run("anchored rejection", func(t *testing.T) {
		s := &agentsState{}
		s.apply(delegateStart("call-1"))
		s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 9000))
		rej := orchLease(orchevents.DelegationRejected, "call-1", "general > coding", "coding", 0, 9000)
		rej.Reason = "budget task_chars exhausted near secret=leakyvalue123 review"
		s.applyOrchEvent(rej)
		row := s.byCall["call-1"]
		if row.State != AgentRejected {
			t.Fatalf("State = %s, want rejected", row.State)
		}
		if strings.Contains(row.Detail, "leakyvalue123") {
			t.Fatalf("Detail not scrubbed: %q", row.Detail)
		}
		if !strings.Contains(row.Detail, "redacted") {
			t.Fatalf("Detail lost the redaction marker: %q", row.Detail)
		}
		if row.EndedAt.IsZero() {
			t.Fatal("rejected row not stamped terminal")
		}
		// The runtime feed's late finish never reverses the rejection.
		s.apply(delegateFinish("call-1", finishedContent))
		if row.State != AgentRejected {
			t.Fatalf("late runtime finish changed rejected row: %s", row.State)
		}
	})

	t.Run("unanchored rejection creates nothing", func(t *testing.T) {
		s := &agentsState{}
		s.applyOrchEvent(orchLease(orchevents.DelegationRejected, "refused-1", "general > coding", "coding", 0, 9000))
		if len(s.rows) != 0 {
			t.Fatalf("rejection created rows: %v", labels(s.rows))
		}
	})
}

func TestOrchBudgetExhaustedIsNotTerminal(t *testing.T) {
	s := &agentsState{}
	s.apply(delegateStart("call-1"))
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 1))
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-1", "general > coding", "coding", "run-1", 1, 5))
	bud := orchChild(orchevents.BudgetExhausted, "call-1", "general > coding", "coding", "run-1", 1, 5)
	bud.Budget = orchevents.BudgetTotalTurns
	bud.BudgetLimit = 4
	bud.BudgetValue = 5
	bud.Reason = "aggregate child-turn budget exhausted (5 turns across all children, limit 4)"
	s.applyOrchEvent(bud)
	row := s.byChildRun["run-1"]
	if row.State.terminal() {
		t.Fatalf("budget exhaust ended the row: %s", row.State)
	}
	if row.State != AgentRunning || row.Turns != 5 {
		t.Fatalf("row lost live state: %s turns=%d", row.State, row.Turns)
	}
	// The real terminal still arrives later (honest cancellation).
	done := orchChild(orchevents.ChildCompleted, "call-1", "general > coding", "coding", "run-1", 1, 6)
	done.Outcome = orchevents.OutcomeCancelled
	s.applyOrchEvent(done)
	if row.State != AgentCancelled {
		t.Fatalf("State = %s after honest cancellation", row.State)
	}
}

// --- live tool telemetry -------------------------------------------------------------

func TestOrchToolCorrelation(t *testing.T) {
	s := &agentsState{}
	s.apply(delegateStart("call-1"))
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 1))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-2", "general > coding", "coding", "run-2", 1, 0))

	// A nested sibling child for the cross-child isolation checks.
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-2", "general > coding > c2", "coding", 1, 1))
	other := s.byOrchCall[orchCallKey("call-2", "general > coding > c2")]
	if other == nil {
		t.Fatal("other child missing")
	}

	s.applyOrchEvent(orchToolEvent(orchevents.ToolStarted, "call-1", "general > coding", "read_file", "run-1", 1))
	row := s.byChildRun["run-1"]
	if row.ActiveTool != "read_file" || row.ToolCalls != 1 {
		t.Fatalf("ActiveTool=%q ToolCalls=%d", row.ActiveTool, row.ToolCalls)
	}
	// Duplicate start for the same operation: no double count.
	s.applyOrchEvent(orchToolEvent(orchevents.ToolStarted, "call-1", "general > coding", "read_file", "run-1", 1))
	if row.ToolCalls != 1 {
		t.Fatalf("ToolCalls = %d, want one per started operation", row.ToolCalls)
	}
	// Another child's tool completion must not clear this row's file.
	s.applyOrchEvent(orchToolEvent(orchevents.ToolCompleted, "call-2", "general > coding > c2", "read_file", "run-2", 2))
	if row.ActiveTool != "read_file" {
		t.Fatalf("another child cleared the active tool: %q", row.ActiveTool)
	}
	if other.ActiveTool != "" {
		t.Fatalf("un-started child shows an active tool: %q", other.ActiveTool)
	}
	// The matching completion clears it.
	s.applyOrchEvent(orchToolEvent(orchevents.ToolCompleted, "call-1", "general > coding", "read_file", "run-1", 1))
	if row.ActiveTool != "" || row.ToolCalls != 1 {
		t.Fatalf("completion state: ActiveTool=%q ToolCalls=%d", row.ActiveTool, row.ToolCalls)
	}
	// Failure and denial counters, with a matching real gap: a failed
	// completion increments only the failing child's counters.
	s.applyOrchEvent(orchToolEvent(orchevents.ToolStarted, "call-1", "general > coding", "shell", "run-1", 1))
	s.applyOrchEvent(orchToolEvent(orchevents.ToolFailed, "call-1", "general > coding", "shell", "run-1", 1))
	s.applyOrchEvent(orchToolEvent(orchevents.ToolDenied, "call-1", "general > coding", "write_file", "run-1", 1))
	if row.FailedCount != 1 || row.DeniedCount != 1 {
		t.Fatalf("failed=%d denied=%d", row.FailedCount, row.DeniedCount)
	}
	if other.FailedCount != 0 || other.DeniedCount != 0 {
		t.Fatal("counters leaked into the other child")
	}
}

func TestOrchLateToolNeverRevivesTerminalRow(t *testing.T) {
	s := &agentsState{}
	s.apply(delegateStart("call-1"))
	done := orchChild(orchevents.ChildCompleted, "call-1", "general > coding", "coding", "run-1", 1, 3)
	done.Outcome = orchevents.OutcomeDone
	s.applyOrchEvent(done)
	row := s.byChildRun["run-1"]

	s.applyOrchEvent(orchToolEvent(orchevents.ToolStarted, "call-1", "general > coding", "shell", "run-1", 1))
	if row.ActiveTool != "" {
		t.Fatalf("late tool start revived active display: %q", row.ActiveTool)
	}
	// Terminal counters still adopt upward, display does not move.
	if row.State != AgentCompleted {
		t.Fatalf("State = %s", row.State)
	}
}

func TestOrchCompletesViaRuntimeFeedRemainsIntact(t *testing.T) {
	// Sanity: the existing depth-1 runtime behavior is untouched by
	// the orch extension, including its collapse summary.
	s := &agentsState{}
	s.apply(delegateStart("call-1"))
	s.apply(delegateTurn("call-1", 1))
	s.apply(delegateTurn("call-1", 2))
	s.apply(delegateFinish("call-1", finishedContent))
	row := s.byCall["call-1"]
	if row.Turns != 2 || row.FinalTurns != 7 || row.TurnsExact {
		t.Fatalf("runtime feed turns regressed: turns=%d final=%d exact=%v", row.Turns, row.FinalTurns, row.TurnsExact)
	}
	if row.State != AgentCompleted || row.Status == "" {
		t.Fatalf("runtime feed terminal regressed: %s %q", row.State, row.Status)
	}
	if got := s.drainCollapsed(); len(got) != 1 {
		t.Fatalf("collapse summary missing: %v", got)
	}

	// Structured events for a different child never disturb it.
	s.applyOrchEvent(orchChild(orchevents.ChildProgress, "call-8", "general > cyber", "cyber", "run-8", 1, 9))
	if row.Turns != 2 {
		t.Fatalf("unrelated progress moved the finished row: %d", row.Turns)
	}
}

func TestOrchNilStateSafety(t *testing.T) {
	var s *agentsState
	s.applyOrchEvent(orchLease(orchevents.DelegationRequested, "call-1", "general > coding", "coding", 0, 1))
	if s.active() {
		t.Fatal("nil state must stay inert")
	}
}

// --- quadrant slot manager -----------------------------------------------------------

func TestSlotsZeroToEight(t *testing.T) {
	s, advance := testClock()
	var sl [3]*childRow
	sl = s.assignQuadrantSlots()
	for i, o := range sl {
		if o != nil {
			t.Fatalf("empty state pinned slot %d to %v", i+1, o)
		}
	}
	if line := s.overflowLine(); line != "" {
		t.Fatalf("empty overflow line = %q", line)
	}

	// One row at a time, oldest claims first.
	var want [3]*childRow
	for i := 0; i < 8; i++ {
		advance(time.Second)
		row := slotRow(s, AgentRunning, s.clock())
		row.Turns = i + 1 // synthetic live progress for the row itself
		sl = s.assignQuadrantSlots()
		if row.State != AgentRunning {
			t.Fatalf("slot battery must not depend on orch state flips: %s", row.State)
		}
		// Track the first three by started order.
		if i < 3 {
			want[i] = row
		}
		for j := 0; j < 3 && j <= i; j++ {
			if sl[j] != want[j] {
				t.Fatalf("after %d rows slot %d = %v, want %v (stable pinning)", i+1, j+1, nameOf(sl[j]), nameOf(want[j]))
			}
		}
		// Slots never shuffle when repeated immediately.
		again := s.assignQuadrantSlots()
		for j := 0; j < 3; j++ {
			if again[j] != sl[j] {
				t.Fatalf("repeated assignment reshuffled slot %d", j+1)
			}
		}
	}

	// Overflow holds exactly the last five, running first is already
	// ordered by started time.
	over := s.overflowRows()
	if len(over) != 5 {
		t.Fatalf("overflow rows = %d (%v), want 5", len(over), labels(over))
	}
	for _, r := range over {
		if r.SlotIndex != 0 {
			t.Fatalf("overflow row %s claims slot %d", r.Label, r.SlotIndex)
		}
	}
}

func nameOf(r *childRow) string {
	if r == nil {
		return "<idle>"
	}
	return r.Label
}

func TestSlotsOverflowLineCorrectness(t *testing.T) {
	s, advance := testClock()
	for i := 0; i < 4; i++ {
		row := slotRow(s, AgentRunning, s.clock())
		row.Turns = 7 + i
		advance(time.Second)
	}
	// Mark the overflowed runner's turn count inexact to exercise the
	// marker: row 4 (subagent-4) is the one beyond the pinned trio.
	s.rows[3].TurnsExact = false
	for i := 0; i < 2; i++ {
		slotRow(s, AgentQueued, s.clock())
		advance(time.Second)
	}
	s.assignQuadrantSlots()

	line := s.overflowLine()
	want := "+1 running: subagent-4 (turn 10+), subagent-5 (queued), subagent-6 (queued)"
	if line != want {
		t.Fatalf("overflowLine =\n%q\nwant\n%q", line, want)
	}
}

func TestSlotsQueuedFirstKeepsSlotAgainstLaterRunning(t *testing.T) {
	s, advance := testClock()
	queued := slotRow(s, AgentQueued, s.clock())
	s.assignQuadrantSlots()
	if queued.SlotIndex != 1 {
		t.Fatalf("queued-first row claimed slot %d, want 1", queued.SlotIndex)
	}
	advance(time.Second)
	running := slotRow(s, AgentRunning, s.clock())
	advance(time.Second)
	runningAgain := slotRow(s, AgentRunning, s.clock())
	s.assignQuadrantSlots()
	if queued.SlotIndex != 1 {
		t.Fatalf("queued owner displaced by running arrival: %d", queued.SlotIndex)
	}
	if running.SlotIndex != 2 || runningAgain.SlotIndex != 3 {
		t.Fatalf("running claims wrong: %d %d", running.SlotIndex, runningAgain.SlotIndex)
	}
}

func TestSlotsQueuedNeverDisplacesRunning(t *testing.T) {
	s, _ := testClock()
	running := []*childRow{slotRow(s, AgentRunning, s.clock()), slotRow(s, AgentRunning, s.clock()), slotRow(s, AgentRunning, s.clock())}
	queued := slotRow(s, AgentQueued, s.clock())

	sl := s.assignQuadrantSlots()
	for i, r := range running {
		if sl[i] != r {
			t.Fatalf("slot %d = %v, want %s", i+1, nameOf(sl[i]), r.Label)
		}
	}
	if over := s.overflowRows(); len(over) != 1 || over[0] != queued {
		t.Fatalf("queued row not in overflow: %v", labels(s.overflowRows()))
	}
	if queued.SlotIndex != 0 {
		t.Fatalf("queued row displaced a slot: %d", queued.SlotIndex)
	}
	// Repeat: nothing moves.
	s.assignQuadrantSlots()
	if queued.SlotIndex != 0 || running[0].SlotIndex != 1 {
		t.Fatal("repeated assignment moved an active row")
	}
}

// finishRow terminalizes a raw slot-battery row at the test clock's
// current time. (Slot-policy tests construct rows directly; the
// state-path batteries cover the event-level terminal policy.)
func finishRow(row *childRow, state AgentRowState, now time.Time) {
	row.State = state
	row.EndedAt = now
	row.stampTerminal(now)
	row.ActiveTool = ""
}

func TestSlotsTerminalReusePrefersAgedOwner(t *testing.T) {
	s, advance := testClock()
	r0 := slotRow(s, AgentRunning, s.clock())
	advance(time.Second)
	r1 := slotRow(s, AgentRunning, s.clock())
	advance(time.Second)
	r2 := slotRow(s, AgentRunning, s.clock())
	s.assignQuadrantSlots()
	if r0.SlotIndex != 1 || r1.SlotIndex != 2 || r2.SlotIndex != 3 {
		t.Fatalf("initial pinning wrong: %d %d %d", r0.SlotIndex, r1.SlotIndex, r2.SlotIndex)
	}

	// r1 completes and ages past the retention window while r0 and r2
	// stay live.
	advance(time.Second)
	finishRow(r1, AgentCompleted, s.clock())
	advance(2 * time.Second)

	// Next claimant with no vacancy: aged r1's slot wins, and the
	// active owners are untouched.
	claimant := slotRow(s, AgentRunning, s.clock())
	s.assignQuadrantSlots()
	if claimant.SlotIndex != 2 {
		t.Fatalf("claimant got slot %d, want aged r1's slot 2", claimant.SlotIndex)
	}
	if r1.SlotIndex != 0 {
		t.Fatalf("archived owner still pinned to slot %d", r1.SlotIndex)
	}
	if r0.SlotIndex != 1 || r2.SlotIndex != 3 {
		t.Fatal("active owner was evicted")
	}
	if s.archivedCount(2) != 1 {
		t.Fatalf("archivedCount(2) = %d, want 1", s.archivedCount(2))
	}
}

func TestSlotsArchivedCountAccumulates(t *testing.T) {
	s, _ := testClock()
	keeper := slotRow(s, AgentRunning, s.clock())
	owner := slotRow(s, AgentRunning, s.clock())
	filler := slotRow(s, AgentRunning, s.clock()) // keeps slot 3 occupied
	s.assignQuadrantSlots()                       // keeper claims slot 1, owner claims slot 2
	if keeper.SlotIndex != 1 || owner.SlotIndex != 2 || filler.SlotIndex != 3 {
		t.Fatalf("initial claim wrong: %d %d %d", keeper.SlotIndex, owner.SlotIndex, filler.SlotIndex)
	}
	finishRow(owner, AgentCompleted, s.clock())
	for i := 1; i <= 3; i++ {
		claimant := slotRow(s, AgentRunning, s.clock())
		s.assignQuadrantSlots()
		if claimant.SlotIndex != 2 {
			t.Fatalf("claim %d missed the reused slot: %d", i, claimant.SlotIndex)
		}
		finishRow(claimant, AgentCompleted, s.clock())
	}
	if got := s.archivedCount(2); got != 3 {
		t.Fatalf("archivedCount(2) = %d, want 3 (each claim archived the prior owner)", got)
	}
	if keeper.SlotIndex != 1 || filler.SlotIndex != 3 {
		t.Fatalf("involved bystanders moved: keeper=%d filler=%d", keeper.SlotIndex, filler.SlotIndex)
	}
}

func TestSlotsRetentionBoundaryFallsBackToOldestTerminal(t *testing.T) {
	s, advance := testClock()
	keeper := slotRow(s, AgentRunning, s.clock()) // slot 1 for life
	youngOld := slotRow(s, AgentRunning, s.clock())
	youngYoung := slotRow(s, AgentRunning, s.clock())
	s.assignQuadrantSlots()
	if keeper.SlotIndex != 1 || youngOld.SlotIndex != 2 || youngYoung.SlotIndex != 3 {
		t.Fatalf("initial pinning wrong: %d %d %d", keeper.SlotIndex, youngOld.SlotIndex, youngYoung.SlotIndex)
	}

	advance(time.Second)
	finishRow(youngOld, AgentCompleted, s.clock()) // terminal at 1s
	advance(500 * time.Millisecond)
	finishRow(youngYoung, AgentFailed, s.clock()) // terminal at 1.5s
	advance(100 * time.Millisecond)               // ages now ~0.6s and ~0.1s: both young

	claimant := slotRow(s, AgentRunning, s.clock())
	// No vacancy exists and no owner has reached 1.5s: the plan's
	// fallback reuses the OLDEST terminal slot anyway.
	s.assignQuadrantSlots()
	if claimant.SlotIndex != 2 {
		t.Fatalf("claimant got slot %d, want oldest-terminal slot 2", claimant.SlotIndex)
	}
	if youngOld.SlotIndex != 0 || youngYoung.SlotIndex != 3 {
		t.Fatalf("young terminal pinning wrong: %d %d", youngOld.SlotIndex, youngYoung.SlotIndex)
	}
	if keeper.SlotIndex != 1 {
		t.Fatal("active owner evicted")
	}
	if s.archivedCount(2) != 1 {
		t.Fatalf("archivedCount(2) = %d", s.archivedCount(2))
	}
}

func TestSlotsEveryTerminalStateEligible(t *testing.T) {
	for _, state := range []AgentRowState{AgentCompleted, AgentFailed, AgentCancelled, AgentDenied, AgentRejected} {
		t.Run(string(state), func(t *testing.T) {
			s, advance := testClock()
			keeper := slotRow(s, AgentRunning, s.clock())
			owner := slotRow(s, AgentRunning, s.clock())
			filler := slotRow(s, AgentRunning, s.clock()) // no vacancy anywhere
			s.assignQuadrantSlots()
			if keeper.SlotIndex != 1 || owner.SlotIndex != 2 || filler.SlotIndex != 3 {
				t.Fatalf("owner never claimed slot 2: %d %d %d", keeper.SlotIndex, owner.SlotIndex, filler.SlotIndex)
			}
			finishRow(owner, state, s.clock())
			advance(2 * time.Second) // seniority past the retention window
			claimant := slotRow(s, AgentQueued, s.clock())

			s.assignQuadrantSlots()
			if claimant.SlotIndex != 2 {
				t.Fatalf("%s slot not reclaimable: claimant slot %d", state, claimant.SlotIndex)
			}
			if keeper.SlotIndex != 1 || filler.SlotIndex != 3 || owner.SlotIndex != 0 {
				t.Fatalf("keeper=%d filler=%d owner=%d", keeper.SlotIndex, filler.SlotIndex, owner.SlotIndex)
			}
			if s.archivedCount(2) != 1 {
				t.Fatalf("archivedCount(2) = %d", s.archivedCount(2))
			}
		})
	}
}

func TestSlotsNestedRowsJoinClaimOrder(t *testing.T) {
	s, advance := testClock()
	// Depth-2 nested row created by the structured feed, claiming in
	// StartedAt order next to a first-level row.
	parent := slotRow(s, AgentRunning, s.clock())
	parent.Depth = 1
	nested := slotRow(s, AgentQueued, s.clock())
	nested.Depth = 2
	nested.ParentLabel = parent.Label
	s.assignQuadrantSlots()
	if parent.SlotIndex != 1 || nested.SlotIndex != 2 {
		t.Fatalf("nested rows ignored by the slot policy: %d %d", parent.SlotIndex, nested.SlotIndex)
	}
	_ = advance
}
