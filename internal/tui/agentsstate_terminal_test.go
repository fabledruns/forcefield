package tui

// Parent-side port of the wave3k-p2 child deliverable (kept its six test
// names and intent). The delivered draft was black-box style: it imported
// forcefield/internal/tui from inside package tui, invented exported
// types (AgentsState, GetRow) that do not exist, and applied terminal
// events without the delegate start that creates a row. Rewritten here
// against the real unexported API in agentsstate.go.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// startCall returns a delegate start event for callID.
func startCall(callID string) runtime.Event {
	return runtime.Event{
		Type: runtime.EventToolStart,
		ToolCall: &providers.ToolCall{
			ID:        callID,
			Name:      "delegate",
			Arguments: map[string]any{"agent": "coding", "task": "task"},
		},
	}
}

const successContent = "[delegate] agent=\"coding\" turns=3 tool_calls=2 failed_tool_calls=0 denied=0\n" +
	"[delegate] child status=partial verification=verified phase=\"done\"\n\nbody text"

func TestAgentsTerminalFinishSuccessCompleted(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	event := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: true, Content: successContent,
	}}
	s.apply(event)
	row := s.byCall["call1"]
	if row.State != AgentCompleted {
		t.Errorf("Expected AgentCompleted, got %v", row.State)
	}
	if !strings.Contains(row.Status, "partial") {
		t.Errorf("Expected Status to contain 'partial', got %s", row.Status)
	}
}

func TestAgentsTerminalFinishErrorFailed(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	event := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: false, IsError: true,
	}}
	s.apply(event)
	row := s.byCall["call1"]
	if row.State != AgentFailed {
		t.Errorf("Expected AgentFailed, got %v", row.State)
	}
	if row.Detail == "" {
		t.Error("Expected Detail to be non-empty, got empty")
	}
}

func TestAgentsTerminalFailedErrDetail(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	err := errors.New("boom")
	event := runtime.Event{Type: runtime.EventToolFailed, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", IsError: true, Err: err,
	}}
	s.apply(event)
	row := s.byCall["call1"]
	if row.State != AgentFailed {
		t.Errorf("Expected AgentFailed, got %v", row.State)
	}
	if row.Detail != "boom" {
		t.Errorf("Expected Detail 'boom', got '%s'", row.Detail)
	}
}

func TestAgentsTerminalRejectionClassification(t *testing.T) {
	tests := map[string]AgentRowState{
		"unknown agent \"x\" (registry miss)":           AgentRejected,
		"task is 30000 characters, exceeding the limit": AgentRejected,
		"delegation cancelled before it started":        AgentCancelled,
		"child run blocked: stop boundary":              AgentFailed,
	}
	for content, expectedState := range tests {
		t.Run(content, func(t *testing.T) {
			s := &agentsState{}
			s.apply(startCall("call1"))
			// Production rejections arrive as soft failures, exactly as
			// softDelegateResult produces them; a SUCCESSFUL result whose
			// text merely mentions these words stays completed.
			event := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
				ToolCallID: "call1", Name: "delegate", Success: false, IsError: true, Content: content,
			}}
			s.apply(event)
			row := s.byCall["call1"]
			if row.State != expectedState {
				t.Errorf("Expected %v, got %v", expectedState, row.State)
			}
		})
	}
}

func TestAgentsTerminalTurnsExactWhenHeaderExceedsObserved(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	// The per-child progress budget let the panel observe turn 3 only.
	s.apply(runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{
		ToolCallID: "call1", Stream: "progress", Data: "[delegate: agent \"coding\" turn 3]",
	}})
	// The child's final header reports a higher total.
	s.apply(runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: true,
		Content: "[delegate] agent=\"coding\" turns=9 tool_calls=2 failed_tool_calls=0 denied=0\n" +
			"[delegate] child status=partial verification=verified phase=\"done\"\n\nbody text",
	}})

	row := s.byCall["call1"]
	if row.Turns != 3 {
		t.Errorf("Expected Turns 3 (the observed count), got %d", row.Turns)
	}
	if row.TurnsExact {
		t.Error("Expected TurnsExact false when the header exceeds what was observed")
	}
}

func TestAgentsTerminalFirstTerminalWins(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	failedEvent := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: false, IsError: true, Content: successContent,
	}}
	s.apply(failedEvent)
	successEvent := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: true, Content: successContent,
	}}
	s.apply(successEvent)

	row := s.byCall["call1"]
	if row.State != AgentFailed {
		t.Errorf("Expected AgentFailed (first terminal wins), got %v", row.State)
	}
}

// TestAgentsPanelChurnResizeAndNewSessionLateEvents is the ORCH-TEST-05
// combined stress test: more than eight rapid delegate children driven
// through the real model Update loop, terminal-width oscillation across
// the 48-column narrow boundary, a /new reset (via NewSession, the exact
// implementation the dispatcher routes "/new" to), and late events from
// the retired run. Central invariant: events from an old stream
// generation must never reach the new session's agents panel — they
// cannot resurrect or mutate state across a reset (even when a new run
// reuses an old call ID), and churn plus resizing never corrupts or
// unbounds the panel rendering.
func TestAgentsPanelChurnResizeAndNewSessionLateEvents(t *testing.T) {
	isolateModelHome(t) // /new persists both session files; keep them off the real FS

	m := newTestModel()
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = sized.(model)
	oldGen := m.streamGen // out of bounds forever once /new retires it

	send := func(m *model, ev runtime.Event, gen uint64) {
		t.Helper()
		next, _ := m.Update(streamEventMsg{Event: ev, gen: gen})
		*m = next.(model)
	}
	resize := func(m *model, width, height int) {
		t.Helper()
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		*m = next.(model)
	}
	bounds := func(m *model, where string) {
		t.Helper()
		if rows := m.agents.agentsPanelRows(); len(rows) > agentsPanelMaxRows {
			t.Fatalf("%s: %d rendered rows exceed cap %d", where, len(rows), agentsPanelMaxRows)
		}
		if h := m.agentsPanelHeight(); h < 0 || h > agentsPanelMaxRows+3 {
			t.Fatalf("%s: agentsPanelHeight = %d, want [0,%d] (cap, +N-more line, header, rule)", where, h, agentsPanelMaxRows+3)
		}
		assertDistinctLabels(t, m, where)
	}

	// Run 1: 12 rapid spawns with the width oscillating mid-churn, as
	// the plan's 120 -> 40 -> 100 sequence states (40 sits below the
	// 48-column narrow boundary, 100 back above it).
	for i := 1; i <= 12; i++ {
		send(&m, startCall(fmt.Sprintf("old-%d", i)), oldGen)
		bounds(&m, fmt.Sprintf("spawn old-%d", i))
		switch i {
		case 4:
			resize(&m, 40, 40)
		case 8:
			resize(&m, 100, 40)
		}
	}
	bounds(&m, "after 12 spawns")

	// Peak bound: with no terminal events yet, the >8 cap collapses
	// the four oldest rows into one "+4 more" line.
	out := m.renderAgentsPanel(m.width)
	if !strings.Contains(out, "+4 more") {
		t.Errorf("render at 12 active rows must show the '+4 more' overflow line, got:\n%s", out)
	}
	if !strings.Contains(out, "subagent-12 ") || strings.Contains(out, "subagent-4 ") {
		t.Errorf("the newest rows must win visibility under the cap, got:\n%s", out)
	}

	// Rapid lifecycle churn through the full terminal vocabulary: six
	// completions, a cancellation, a failure, and progress that leaves
	// two children still running while the panel is resized back and
	// forth under load.
	for i := 1; i <= 6; i++ {
		send(&m, completeEvent(fmt.Sprintf("old-%d", i), true), oldGen)
	}
	send(&m, cancelEvent("old-7"), oldGen)
	send(&m, completeEvent("old-8", false), oldGen)
	for i := 9; i <= 12; i++ {
		send(&m, progressEvent(fmt.Sprintf("old-%d", i), i-8), oldGen)
	}
	resize(&m, 120, 40)
	resize(&m, 40, 40)
	resize(&m, 100, 40)
	send(&m, completeEvent("old-9", true), oldGen)
	send(&m, completeEvent("old-10", true), oldGen)
	send(&m, progressEvent("old-11", 9), oldGen)
	send(&m, progressEvent("old-12", 10), oldGen)
	bounds(&m, "after run 1 churn")

	// Completed, cancelled and failed children must hold their terminal
	// states, and exactly the two still-running rows may be active.
	for id, want := range map[string]AgentRowState{
		"old-1":  AgentCompleted,
		"old-9":  AgentCompleted,
		"old-7":  AgentCancelled,
		"old-8":  AgentFailed,
		"old-11": AgentRunning,
		"old-12": AgentRunning,
	} {
		if row := m.agents.byCall[id]; row == nil || row.State != want {
			t.Errorf("%s state = %v, want %v", id, rowState(&m, id), want)
		}
	}
	if got := m.agents.activeRows(); len(got) != 2 {
		t.Fatalf("active rows after churn = %d (labels %v), want exactly old-11 and old-12", len(got), rowLabels(got))
	}
	// Labels were handed out once each: churn must not duplicate or
	// renumber rows.
	if len(m.agents.rows) != 12 || len(m.agents.byCall) != 12 {
		t.Fatalf("rows = %d, byCall = %d, want 12/12 after the churn", len(m.agents.rows), len(m.agents.byCall))
	}
	for i := 1; i <= 12; i++ {
		if row := m.agents.byCall[fmt.Sprintf("old-%d", i)]; row.Label != fmt.Sprintf("subagent-%d", i) {
			t.Errorf("row %d label = %q, want subagent-%d", i, row.Label, i)
		}
	}

	// /new resets the panel exactly: the retired generation is bumped
	// and the state is replaced wholesale, leaving zero rows.
	if err := m.NewSession(); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if m.streamGen != oldGen+1 {
		t.Fatalf("streamGen = %d after /new, want %d (the old run must be retired)", m.streamGen, oldGen+1)
	}
	if m.agents.active() {
		t.Error("agent panel still active after /new, want 0 active rows")
	}
	if len(m.agents.rows) != 0 || len(m.agents.byCall) != 0 || len(m.agents.collapsed) != 0 {
		t.Errorf("agent state survived /new: rows=%d byCall=%d collapsed=%d, all want 0",
			len(m.agents.rows), len(m.agents.byCall), len(m.agents.collapsed))
	}
	if got := m.renderAgentsPanel(120); got != "" {
		t.Errorf("panel rendered after /new:\n%s", got)
	}

	// Run 2 comes up on the fresh generation, including a call ID that
	// the retired run already used — identity is (generation, call ID),
	// never the agent name and never the ID alone.
	newGen := m.streamGen
	for _, id := range []string{"new-1", "new-2", "old-3"} {
		send(&m, startCall(id), newGen)
	}
	if got := m.agents.activeRows(); len(got) != 3 {
		t.Fatalf("run 2 active rows = %d, want 3", len(got))
	}
	// A fresh state means fresh labels: subagent-3 belongs to the new
	// run's reused call, not to run 1's third child.
	if row := m.agents.byCall["old-3"]; row.Label != "subagent-3" || row.Agent != "coding" {
		t.Errorf("reused call got label %q agent %q, want a fresh subagent-3 row", row.Label, row.Agent)
	}

	// Late run-1 events arrive on the wire with the retired generation.
	// Every one of them must be dropped before touching the panel:
	// they cannot spawn, complete, cancel, or fail run-2 rows.
	send(&m, startCall("old-13"), oldGen)
	send(&m, completeEvent("old-1", true), oldGen)
	send(&m, completeEvent("old-3", true), oldGen)  // reused ID, stale generation
	send(&m, cancelEvent("new-2"), oldGen)          // old run may not cancel a new agent
	send(&m, completeEvent("new-1", false), oldGen) // ...nor fail one
	if len(m.agents.rows) != 3 {
		t.Errorf("rows = %d after late run-1 events, want 3 (stale events must not create rows)", len(m.agents.rows))
	}
	if row := m.agents.byCall["old-3"]; row.State != AgentQueued {
		t.Errorf("reused-call row state = %q, want queued (stale run-1 events must not terminalize it)", row.State)
	}
	if row := m.agents.byCall["new-2"]; row.State != AgentQueued {
		t.Errorf("new-2 state = %q, want queued (late old-run cancel must not retire it)", row.State)
	}
	if row := m.agents.byCall["new-1"]; row.State != AgentQueued {
		t.Errorf("new-1 state = %q, want queued (late old-run failure must not retire it)", row.State)
	}
	if summaries := m.agents.drainCollapsed(); len(summaries) != 0 {
		t.Errorf("late old-run events leaked transcript summaries: %v", summaries)
	}

	// The state layer's own attribution contract backs the generation
	// gate up: an event the panel cannot attribute to a delegate call
	// it knows is ignored, so even a hypothetical leak cannot
	// resurrect retired rows.
	stale := m.agents
	before := len(stale.rows)
	stale.apply(completeEvent("old-1", true))
	stale.apply(cancelEvent("never-seen-id"))
	if len(stale.rows) != before {
		t.Errorf("unattributed events changed the panel: %d rows, want %d", len(stale.rows), before)
	}

	// Resizing both directions of the 48-column boundary with run 2 in
	// flight: rendering stays bounded and consistent, and the narrow
	// layout drops the task column while staying ASCII.
	send(&m, progressEvent("new-1", 1), newGen)
	for _, width := range []int{120, 40, 100} {
		resize(&m, width, 40)
		bounds(&m, fmt.Sprintf("resize to %d", width))
	}
	if out := m.renderAgentsPanel(40); strings.Contains(out, "task") || strings.ContainsAny(out, "●○✓✕⊘") {
		t.Errorf("narrow layout must drop the task column and stay ASCII:\n%s", out)
	}
	if out := m.renderAgentsPanel(120); !strings.Contains(out, "task") {
		t.Errorf("wide layout must show the task column:\n%s", out)
	}
	// Width-1 clipping is a pure render concern: nothing may panic and
	// the panel must still render its bound rows.
	_ = m.renderAgentsPanel(1)
}

// completeEvent builds a delegate terminal event: Success reports the
// child's fixed result header, otherwise an error without content.
func completeEvent(id string, success bool) runtime.Event {
	if success {
		return runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
			ToolCallID: id, Name: "delegate", Success: true, Content: successContent,
		}}
	}
	return runtime.Event{Type: runtime.EventToolFailed, ToolResult: &runtime.ToolResult{
		ToolCallID: id, Name: "delegate", IsError: true, Err: errors.New("disk gone"),
	}}
}

// cancelEvent builds the teardown-time cancellation terminal event.
func cancelEvent(id string) runtime.Event {
	return runtime.Event{Type: runtime.EventToolCancelled, ToolResult: &runtime.ToolResult{
		ToolCallID: id, Name: "delegate",
	}}
}

// progressEvent builds a delegate turn-progress event matching the
// recorder’s "[delegate: agent ... turn N]" payload shape.
func progressEvent(id string, turn int) runtime.Event {
	return runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{
		ToolCallID: id, Stream: "progress",
		Data: fmt.Sprintf(`[delegate: agent "coding" turn %d]`, turn),
	}}
}

// rowState reports ro.State or <nil> for an honest failure message.
func rowState(m *model, id string) AgentRowState {
	row := m.agents.byCall[id]
	if row == nil {
		return "<nil>"
	}
	return row.State
}

// rowLabels returns the labels of the given rows, in order.
func rowLabels(rows []*childRow) []string {
	labels := make([]string, 0, len(rows))
	for _, row := range rows {
		labels = append(labels, row.Label)
	}
	return labels
}

// assertDistinctLabels pins the panel's newest-rows-win cap invariant at
// the render level: no rendered row label may appear on two lines,
// whatever the width.
func assertDistinctLabels(t *testing.T, m *model, where string) {
	t.Helper()
	out := m.renderAgentsPanel(m.width)
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, "subagent-")
		if i < 0 {
			continue
		}
		label := line[i:]
		if j := strings.IndexByte(label, ' '); j > 0 {
			label = label[:j]
		}
		if seen[label] {
			t.Fatalf("%s: duplicate rendered row %q", where, line)
		}
		seen[label] = true
	}
}
