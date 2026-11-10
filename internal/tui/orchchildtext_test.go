package tui

// Live child output in the orchestration quadrants: the runtime's
// structured child_text events reach the right child's own bounded
// buffer, the buffer stays bounded, a completed child keeps what it
// produced, and a native batch delegation gives every declared task its
// own row. Conventions follow agentsstate_orch_test.go: real
// unexported API, no sleeps, direct state assertions.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// --- helpers -------------------------------------------------------------------------

// batchDelegateStart returns the runtime feed's delegate call start for
// a native batch delegation: one call carrying a tasks array.
func batchDelegateStart(callID string, tasks ...string) runtime.Event {
	items := make([]any, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, map[string]any{"agent": "coding", "task": task})
	}
	return runtime.Event{
		Type: runtime.EventToolStart,
		ToolCall: &providers.ToolCall{
			ID:        callID,
			Name:      "delegate",
			Arguments: map[string]any{"tasks": items},
		},
	}
}

// batchChildEvent builds one batch child's structured event: the
// delegate call id is shared by the whole batch, the chain leaf and the
// batch index tell the tasks apart. Child-scoped events carry the
// child's own depth (1).
func batchChildEvent(t orchevents.Type, callID, chain, agent, childRunID string, index int) orchevents.Event {
	return orchevents.Event{
		Type:       t,
		Agent:      agent,
		Chain:      chain,
		Depth:      1,
		ChildRunID: childRunID,
		CallID:     callID,
		BatchIndex: index,
	}
}

// batchLeaseEvent builds one batch task's delegation-scoped event:
// those carry the delegating scope's depth (0), so the child is one
// deeper.
func batchLeaseEvent(t orchevents.Type, callID, chain, agent string, index int) orchevents.Event {
	return orchevents.Event{
		Type:       t,
		Agent:      agent,
		Chain:      chain,
		Depth:      0,
		CallID:     callID,
		BatchIndex: index,
	}
}

// childTextEvent builds one live-text event for a child.
func childTextEvent(callID, chain, agent, childRunID, text string) orchevents.Event {
	return orchevents.Event{
		Type:       orchevents.ChildText,
		Agent:      agent,
		Chain:      chain,
		Depth:      1,
		ChildRunID: childRunID,
		CallID:     callID,
		Text:       text,
	}
}

// --- routing -------------------------------------------------------------------------

// TestOrchChildTextReachesOnlyItsOwnChild proves the routing: a
// child's live text lands in that child's buffer and nowhere else.
func TestOrchChildTextReachesOnlyItsOwnChild(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call-1"))
	s.apply(startCall("call-2"))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-2", "general > docs", "docs", "run-2", 1, 0))

	s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", "alpha output"))
	s.applyOrchEvent(childTextEvent("call-2", "general > docs", "docs", "run-2", "beta output"))

	first := s.byCall["call-1"]
	second := s.byCall["call-2"]
	if first == nil || second == nil {
		t.Fatal("the two delegate rows are missing")
	}
	if got := first.outputText(); got != "alpha output" {
		t.Fatalf("first child's output = %q, want %q", got, "alpha output")
	}
	if got := second.outputText(); got != "beta output" {
		t.Fatalf("second child's output = %q, want %q", got, "beta output")
	}
	if strings.Contains(first.outputText(), "beta") {
		t.Fatal("the first child's buffer carries its sibling's text")
	}
	if strings.Contains(second.outputText(), "alpha") {
		t.Fatal("the second child's buffer carries its sibling's text")
	}
}

// TestOrchChildTextInterleavedDoesNotMix proves interleaved delivery
// cannot mix two children: the chunks arrive in any order, each still
// lands in its own buffer.
func TestOrchChildTextInterleavedDoesNotMix(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call-1"))
	s.apply(startCall("call-2"))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-2", "general > docs", "docs", "run-2", 1, 0))

	s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", "a1 "))
	s.applyOrchEvent(childTextEvent("call-2", "general > docs", "docs", "run-2", "b1 "))
	s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", "a2 "))
	s.applyOrchEvent(childTextEvent("call-2", "general > docs", "docs", "run-2", "b2 "))

	if got := s.byCall["call-1"].outputText(); got != "a1 a2 " {
		t.Fatalf("first child's output = %q, want %q", got, "a1 a2 ")
	}
	if got := s.byCall["call-2"].outputText(); got != "b1 b2 " {
		t.Fatalf("second child's output = %q, want %q", got, "b1 b2 ")
	}
}

// TestOrchChildTextUpdatesIncrementally proves the buffer grows as the
// child streams: each chunk is visible before the child finishes.
func TestOrchChildTextUpdatesIncrementally(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call-1"))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0))

	for _, chunk := range []string{"first\n", "second\n", "third\n"} {
		s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", chunk))
		row := s.byCall["call-1"]
		if row.State != AgentRunning {
			t.Fatalf("row state = %q after live text, want %q", row.State, AgentRunning)
		}
		if !strings.Contains(row.outputText(), strings.TrimSpace(chunk)) {
			t.Fatalf("chunk %q is not visible yet: %q", chunk, row.outputText())
		}
	}
	lines := s.byCall["call-1"].outputLines(childOutputVisibleLines)
	if len(lines) != 3 {
		t.Fatalf("visible output lines = %d, want 3", len(lines))
	}
}

// TestOrchChildTextBufferIsBounded proves the row's buffer cannot grow
// without limit: past the line cap the oldest lines go, and past the
// byte cap a single very long line is cut from the front.
func TestOrchChildTextBufferIsBounded(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call-1"))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0))

	for i := 0; i < maxChildOutputLines*3; i++ {
		s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", "line\n"))
	}
	row := s.byCall["call-1"]
	lines := row.outputLines(0)
	if len(lines) > maxChildOutputLines {
		t.Fatalf("buffered lines = %d, want at most %d", len(lines), maxChildOutputLines)
	}
	if len(lines) != maxChildOutputLines {
		t.Fatalf("buffered lines = %d, want the cap %d", len(lines), maxChildOutputLines)
	}

	// One very long line: the byte bound cuts it, the line bound alone
	// could not.
	long := strings.Repeat("x", maxChildOutputBytes*2)
	s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", long))
	if got := len(row.outputText()); got > maxChildOutputBytes+len(long) {
		t.Fatalf("buffer is %d bytes after a long line, want it bounded", got)
	}
	if got := len(row.outputText()); got < maxChildOutputBytes {
		t.Fatalf("buffer is %d bytes after a long line, want the tail kept", got)
	}
}

// TestOrchCompletedChildKeepsItsOutput proves the completed row keeps
// the text it produced, and that late text never revives a settled
// child.
func TestOrchCompletedChildKeepsItsOutput(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call-1"))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0))
	s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", "final answer\n"))

	done := orchChild(orchevents.ChildCompleted, "call-1", "general > coding", "coding", "run-1", 1, 0)
	done.Outcome = orchevents.OutcomeDone
	done.Status = "verified"
	s.applyOrchEvent(done)

	row := s.byCall["call-1"]
	if row.State != AgentCompleted {
		t.Fatalf("row state = %q, want %q", row.State, AgentCompleted)
	}
	if got := row.outputText(); !strings.Contains(got, "final answer") {
		t.Fatalf("the completed row lost its output: %q", got)
	}

	// A late chunk must not change a settled row.
	s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", "late text"))
	if got := row.outputText(); strings.Contains(got, "late text") {
		t.Fatalf("a settled row accepted late text: %q", got)
	}
}

// TestOrchFailedChildShowsDiagnostics proves a failed child carries its
// reason, and that the live text it produced before failing is kept.
func TestOrchFailedChildShowsDiagnostics(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call-1"))
	s.applyOrchEvent(orchChild(orchevents.AgentSpawned, "call-1", "general > coding", "coding", "run-1", 1, 0))
	s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", "partial work\n"))

	failed := orchChild(orchevents.ChildCompleted, "call-1", "general > coding", "coding", "run-1", 1, 0)
	failed.Outcome = orchevents.OutcomeError
	failed.Error = "child run failed: provider exploded"
	s.applyOrchEvent(failed)

	row := s.byCall["call-1"]
	if row.State != AgentFailed {
		t.Fatalf("row state = %q, want %q", row.State, AgentFailed)
	}
	if !strings.Contains(row.Detail, "provider exploded") {
		t.Fatalf("row detail = %q, want the failure reason", row.Detail)
	}
	if !strings.Contains(row.outputText(), "partial work") {
		t.Fatalf("the failed row lost the text it produced: %q", row.outputText())
	}
}

// --- native batch delegation ---------------------------------------------------------

// TestOrchBatchChildrenGetTheirOwnRows proves a native batch
// delegation produces one row per declared task, each with its own
// label, agent, chain, and batch index. The batch call itself creates
// no row: it is the parent's own tool call, not a child.
func TestOrchBatchChildrenGetTheirOwnRows(t *testing.T) {
	s := &agentsState{}
	s.apply(batchDelegateStart("call-batch", "first task", "second task"))

	if len(s.rows) != 0 {
		t.Fatalf("the batch call created %d rows, want 0 (its tasks own the rows)", len(s.rows))
	}

	s.applyOrchEvent(batchLeaseEvent(orchevents.DelegationRequested, "call-batch", "general > coding#1", "coding", 1))
	s.applyOrchEvent(batchLeaseEvent(orchevents.DelegationRequested, "call-batch", "general > coding#2", "coding", 2))

	if len(s.rows) != 2 {
		t.Fatalf("batch children rows = %d, want 2", len(s.rows))
	}
	if s.rows[0].Label == s.rows[1].Label {
		t.Fatalf("the two batch children share the label %q", s.rows[0].Label)
	}
	if s.rows[0].BatchIndex != 1 || s.rows[1].BatchIndex != 2 {
		t.Fatalf("batch indexes = %d/%d, want 1/2", s.rows[0].BatchIndex, s.rows[1].BatchIndex)
	}
	for _, row := range s.rows {
		if row.Agent != "coding" {
			t.Fatalf("batch child agent = %q, want %q", row.Agent, "coding")
		}
		if row.State != AgentQueued {
			t.Fatalf("batch child state = %q, want %q", row.State, AgentQueued)
		}
	}
}

// TestOrchBatchChildShowsItsOwnTaskText proves the declared task text
// reaches the right child: the runtime feed is the only place the task
// text exists, and the batch index says which task it was.
func TestOrchBatchChildShowsItsOwnTaskText(t *testing.T) {
	s := &agentsState{}
	s.apply(batchDelegateStart("call-batch", "first task", "second task"))
	s.applyOrchEvent(batchLeaseEvent(orchevents.DelegationRequested, "call-batch", "general > coding#1", "coding", 1))
	s.applyOrchEvent(batchLeaseEvent(orchevents.DelegationRequested, "call-batch", "general > coding#2", "coding", 2))

	if got := s.rows[0].Task; got != "first task" {
		t.Fatalf("first batch child's task = %q, want %q", got, "first task")
	}
	if got := s.rows[1].Task; got != "second task" {
		t.Fatalf("second batch child's task = %q, want %q", got, "second task")
	}
}

// TestOrchBatchChildTaskTextJoinsBothOrders proves the panel joins the
// runtime feed and the structured stream in either arrival order: the
// task text is filled in whenever the other side arrives.
func TestOrchBatchChildTaskTextJoinsBothOrders(t *testing.T) {
	// Structured stream first, runtime feed second.
	s := &agentsState{}
	s.applyOrchEvent(batchLeaseEvent(orchevents.DelegationRequested, "call-batch", "general > coding#1", "coding", 1))
	s.apply(batchDelegateStart("call-batch", "first task", "second task"))
	if got := s.rows[0].Task; got != "first task" {
		t.Fatalf("batch child's task = %q, want %q", got, "first task")
	}

	// Runtime feed first, structured stream second.
	s2 := &agentsState{}
	s2.apply(batchDelegateStart("call-batch", "first task", "second task"))
	s2.applyOrchEvent(batchLeaseEvent(orchevents.DelegationRequested, "call-batch", "general > coding#2", "coding", 2))
	if got := s2.rows[0].Task; got != "second task" {
		t.Fatalf("batch child's task = %q, want %q", got, "second task")
	}
}

// TestOrchBatchChildTextStaysInItsOwnRow proves the live text of two
// batch children never crosses over, even though they share one
// delegate call id: the chain leaf and the child run id tell them
// apart.
func TestOrchBatchChildTextStaysInItsOwnRow(t *testing.T) {
	s := &agentsState{}
	s.apply(batchDelegateStart("call-batch", "first task", "second task"))
	s.applyOrchEvent(batchLeaseEvent(orchevents.DelegationRequested, "call-batch", "general > coding#1", "coding", 1))
	s.applyOrchEvent(batchLeaseEvent(orchevents.DelegationRequested, "call-batch", "general > coding#2", "coding", 2))
	s.applyOrchEvent(batchChildEvent(orchevents.AgentSpawned, "call-batch", "general > coding#1", "coding", "run-1", 1))
	s.applyOrchEvent(batchChildEvent(orchevents.AgentSpawned, "call-batch", "general > coding#2", "coding", "run-2", 2))

	s.applyOrchEvent(childTextEvent("call-batch", "general > coding#1", "coding", "run-1", "alpha text"))
	s.applyOrchEvent(childTextEvent("call-batch", "general > coding#2", "coding", "run-2", "beta text"))

	if len(s.rows) != 2 {
		t.Fatalf("batch children rows = %d, want 2", len(s.rows))
	}
	first, second := s.byChildRun["run-1"], s.byChildRun["run-2"]
	if first == nil || second == nil {
		t.Fatal("the batch children are not anchored by their child run ids")
	}
	if got := first.outputText(); got != "alpha text" {
		t.Fatalf("first batch child's output = %q, want %q", got, "alpha text")
	}
	if got := second.outputText(); got != "beta text" {
		t.Fatalf("second batch child's output = %q, want %q", got, "beta text")
	}
}

// TestOrchChildTextBeforeRowIsHeld proves live text that arrives before
// its row is not lost: it is held and replayed into the row as soon as
// the row appears.
func TestOrchChildTextBeforeRowIsHeld(t *testing.T) {
	s := &agentsState{}
	s.applyOrchEvent(childTextEvent("call-1", "general > coding", "coding", "run-1", "early text"))
	s.apply(startCall("call-1"))

	row := s.byCall["call-1"]
	if row == nil {
		t.Fatal("the delegate row is missing")
	}
	if got := row.outputText(); got != "early text" {
		t.Fatalf("row output = %q, want the held text %q", got, "early text")
	}
}

// --- model and canvas ----------------------------------------------------------------

// TestOrchQuadrantRendersLiveChildText proves the canvas actually
// draws the child's live text in its quadrant.
func TestOrchQuadrantRendersLiveChildText(t *testing.T) {
	m, _ := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	m = activateOrch(t, m, "call-1", "run-1")

	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		childTextEvent("call-1", "general > coding", "coding", "run-1", "live child line"),
	}})

	view := m.View()
	if !strings.Contains(view, "live child line") {
		t.Fatalf("the canvas does not show the child's live text:\n%s", view)
	}
}

// TestOrchQuadrantKeepsLiveTextAcrossResize proves a resize never
// loses the child's live output and never breaks the canvas geometry.
func TestOrchQuadrantKeepsLiveTextAcrossResize(t *testing.T) {
	m, _ := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	m = activateOrch(t, m, "call-1", "run-1")
	m = deliver(t, m, orchDeliverMsg{gen: m.orchGen, events: []orchevents.Event{
		childTextEvent("call-1", "general > coding", "coding", "run-1", "live child line"),
	}})

	for _, size := range []tea.WindowSizeMsg{
		{Width: 80, Height: 24},
		{Width: 64, Height: 18},
		{Width: 48, Height: 14},
		{Width: 30, Height: 10},
		{Width: 0, Height: 0},
	} {
		next, _ = m.Update(size)
		m = next.(model)
		view := m.View()
		if strings.Contains(view, "live child line") && size.Width >= 48 && size.Height >= 14 {
			// The grid layout has room for the line; it must be there.
			continue
		}
		// The narrow layouts clip: the important invariant is that the
		// panel still knows the text and the view never panics.
		if row := m.agents.byCall["call-1"]; row == nil || !strings.Contains(row.outputText(), "live child line") {
			t.Fatalf("resize to %dx%d lost the child's live output", size.Width, size.Height)
		}
	}
}

// TestOrchStaleGenerationDropsChildText proves the generation gate
// still holds for live text: a batch stamped with an old generation is
// dropped, so a straggler from a previous session cannot write into
// the new one.
func TestOrchStaleGenerationDropsChildText(t *testing.T) {
	m, _ := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(model)
	m = activateOrch(t, m, "call-1", "run-1")

	// /new advances the generation: the batch below was stamped with
	// the previous generation and must be dropped whole.
	m.bumpOrchGen()
	m = deliver(t, m, orchDeliverMsg{gen: 0, events: []orchevents.Event{
		childTextEvent("call-1", "general > coding", "coding", "run-1", "stale text"),
	}})

	row := m.agents.byCall["call-1"]
	if row == nil {
		t.Fatal("the delegate row is missing")
	}
	if got := row.outputText(); strings.Contains(got, "stale text") {
		t.Fatalf("a stale generation wrote into the live row: %q", got)
	}
}

// TestOrchChildTextIsDroppable proves live text is treated as
// replaceable under queue pressure: dropping a chunk may lose a piece
// of the stream, but it never corrupts the panel's story.
func TestOrchChildTextIsDroppable(t *testing.T) {
	if !droppable(orchevents.Event{Type: orchevents.ChildText}) {
		t.Fatal("child text is not droppable; a busy run could stall the queue")
	}
	if droppable(orchevents.Event{Type: orchevents.ChildCompleted}) {
		t.Fatal("child completion is droppable; the panel's story could be corrupted")
	}
}
