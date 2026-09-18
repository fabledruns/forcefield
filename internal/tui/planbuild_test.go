package tui

import (
	"strings"
	"testing"

	"forcefield/internal/session"
)

// TestStartPlan_BeginsPlanTurn is the smallest StartPlan behavior: an idle
// model opens a planning turn through the normal stream path.
func TestStartPlan_BeginsPlanTurn(t *testing.T) {
	m := newSubmitTestModel(t)
	t.Cleanup(func() { m.stopStream(true) })
	if err := m.StartPlan("add a flag"); err != nil {
		t.Fatalf("StartPlan() error = %v", err)
	}

	if !m.waiting {
		t.Fatal("StartPlan did not mark the model waiting")
	}
	if m.turnKind != turnPlan {
		t.Fatalf("turnKind = %v, want turnPlan", m.turnKind)
	}
	if m.stream == nil {
		t.Fatal("StartPlan did not open a stream")
	}
	last := m.entries[len(m.entries)-1]
	if last.Role != roleUser || last.Content != "add a flag" {
		t.Fatalf("last entry = %+v, want the plan task as user", last)
	}
}

// TestStartPlan_BusyErrors keeps turns serialized: a second turn cannot
// start on top of a live run.
func TestStartPlan_BusyErrors(t *testing.T) {
	m := newSubmitTestModel(t)
	m.waiting = true
	if err := m.StartPlan("another"); err == nil {
		t.Fatal("StartPlan during a live run did not return an error")
	}
	if m.stream != nil {
		t.Fatal("StartPlan during a live run opened a stream")
	}
}

// TestStartBuild_NoPlanErrors keeps the no-plan failure visible at the
// context boundary.
func TestStartBuild_NoPlanErrors(t *testing.T) {
	m := newSubmitTestModel(t)
	err := m.StartBuild()
	if err == nil || !strings.Contains(err.Error(), "No plan yet") {
		t.Fatalf("StartBuild() error = %v, want the no-plan error", err)
	}
}

// TestStartBuild_BeginsBuildTurn hands the accepted plan to a normal agent
// turn and marks it building.
func TestStartBuild_BeginsBuildTurn(t *testing.T) {
	m := newSubmitTestModel(t)
	t.Cleanup(func() { m.stopStream(true) })
	m.session.Plan = &session.PlanState{Body: "1. Add the flag.", Status: session.PlanDraft}
	if err := m.StartBuild(); err != nil {
		t.Fatalf("StartBuild() error = %v", err)
	}

	if !m.waiting {
		t.Fatal("StartBuild did not mark the model waiting")
	}
	if m.turnKind != turnBuild {
		t.Fatalf("turnKind = %v, want turnBuild", m.turnKind)
	}
	if m.session.Plan.Status != session.PlanBuilding {
		t.Fatalf("plan status = %q, want building", m.session.Plan.Status)
	}
	last := m.entries[len(m.entries)-1]
	if last.Role != roleUser || !strings.Contains(last.Content, "1. Add the flag.") {
		t.Fatalf("build turn does not carry the plan body: %+v", last)
	}
}

// TestPlanDone_PersistsPlan is the plan accept step: a finished planning
// turn stores its text as the session's draft plan.
func TestPlanDone_PersistsPlan(t *testing.T) {
	m := newSubmitTestModel(t)
	m.turnKind = turnPlan
	m.planBuffer = "1. first\n2. second"

	next, _ := m.Update(streamDoneMsg{gen: m.streamGen})
	got := next.(model)

	plan := got.session.Plan
	if plan == nil || plan.Body != "1. first\n2. second" || plan.Status != session.PlanDraft {
		t.Fatalf("persisted plan = %+v, want the draft body", plan)
	}
	joined := entriesText(got.entries)
	if !strings.Contains(joined, "Plan saved") {
		t.Fatalf("transcript missing plan confirmation:\n%s", joined)
	}
}

// TestBuildDone_MarksDone closes a build turn as done.
func TestBuildDone_MarksDone(t *testing.T) {
	m := newSubmitTestModel(t)
	m.session.Plan = &session.PlanState{Body: "steps", Status: session.PlanBuilding}
	m.turnKind = turnBuild

	next, _ := m.Update(streamDoneMsg{gen: m.streamGen})
	got := next.(model)

	if got.session.Plan.Status != session.PlanDone {
		t.Fatalf("plan status = %q, want done", got.session.Plan.Status)
	}
	if joined := entriesText(got.entries); !strings.Contains(joined, "Build complete") {
		t.Fatalf("transcript missing build confirmation:\n%s", joined)
	}
}

// TestBuildCancelled_MarksPartial keeps partial execution explicit when a
// build turn is cancelled.
func TestBuildCancelled_MarksPartial(t *testing.T) {
	m := newSubmitTestModel(t)
	m.session.Plan = &session.PlanState{Body: "steps", Status: session.PlanBuilding}
	m.turnKind = turnBuild

	next, _ := m.Update(streamCancelledMsg{gen: m.streamGen})
	got := next.(model)

	if got.session.Plan.Status != session.PlanPartial {
		t.Fatalf("plan status = %q, want partial", got.session.Plan.Status)
	}
}

// TestPlanCancelled_SavesNothing keeps a cancelled planning turn out of
// the plan state: only finished (or stopped) turns persist.
func TestPlanCancelled_SavesNothing(t *testing.T) {
	m := newSubmitTestModel(t)
	m.turnKind = turnPlan
	m.planBuffer = "half a plan"

	next, _ := m.Update(streamCancelledMsg{gen: m.streamGen})
	got := next.(model)

	if got.session.Plan != nil {
		t.Fatalf("cancelled plan turn persisted %+v, want nil", got.session.Plan)
	}
}

// TestPlanBlocked_SavesDraft keeps a stopped planning turn's text as a
// draft instead of dropping it.
func TestPlanBlocked_SavesDraft(t *testing.T) {
	m := newSubmitTestModel(t)
	m.turnKind = turnPlan
	m.planBuffer = "partial outline"

	next, _ := m.Update(streamBlockedMsg{gen: m.streamGen})
	got := next.(model)

	plan := got.session.Plan
	if plan == nil || plan.Body != "partial outline" || plan.Status != session.PlanDraft {
		t.Fatalf("blocked plan turn persisted %+v, want the draft", plan)
	}
}

// TestPlanDone_EmptyBodySavesNothing reports an empty planning turn
// instead of storing an empty plan.
func TestPlanDone_EmptyBodySavesNothing(t *testing.T) {
	m := newSubmitTestModel(t)
	m.turnKind = turnPlan
	m.planBuffer = "  \n "

	next, _ := m.Update(streamDoneMsg{gen: m.streamGen})
	got := next.(model)

	if got.session.Plan != nil {
		t.Fatalf("empty plan turn persisted %+v, want nil", got.session.Plan)
	}
	if joined := entriesText(got.entries); !strings.Contains(joined, "no plan saved") {
		t.Fatalf("transcript missing empty-plan notice:\n%s", joined)
	}
}

// TestTurnStartedMsg_PumpsActiveStream gives slash-command turns the same
// event pump chat submits get.
func TestTurnStartedMsg_PumpsActiveStream(t *testing.T) {
	m := newSubmitTestModel(t)
	t.Cleanup(func() { m.stopStream(true) })
	if err := m.StartPlan("task"); err != nil {
		t.Fatalf("StartPlan() error = %v", err)
	}
	if _, cmd := m.Update(turnStartedMsg{gen: m.streamGen}); cmd == nil {
		t.Fatal("turnStartedMsg for the active stream returned no pump command")
	}
	if _, cmd := m.Update(turnStartedMsg{gen: m.streamGen + 99}); cmd != nil {
		t.Fatal("stale turnStartedMsg returned a pump command")
	}
}

// TestStartBuild_WarnsOnDrift warns but proceeds when the conversation
// grew since planning.
func TestStartBuild_WarnsOnDrift(t *testing.T) {
	m := newSubmitTestModel(t)
	t.Cleanup(func() { m.stopStream(true) })
	m.session.AddMessage("user", "original task")
	m.session.Plan = &session.PlanState{Body: "steps", Status: session.PlanDraft, BaseMsgCount: 1}
	m.session.AddMessage("user", "more talk")
	m.session.AddMessage("assistant", "more answer")

	if err := m.StartBuild(); err != nil {
		t.Fatalf("StartBuild() error = %v, want warn-and-proceed", err)
	}

	if joined := entriesText(m.entries); !strings.Contains(joined, "WARNING") {
		t.Fatalf("transcript missing drift warning:\n%s", joined)
	}
	if m.session.Plan.Status != session.PlanBuilding {
		t.Fatalf("plan status = %q, want building after warn-and-proceed", m.session.Plan.Status)
	}
}

func entriesText(entries []chatEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Content)
		b.WriteString("\n")
	}
	return b.String()
}
