package task

import (
	"context"
	"strings"
	"testing"
)

func TestState_FinalStatus_NoToolsIsVerified(t *testing.T) {
	st := New("say hello")
	if got := st.FinalStatus(); got != StatusVerified {
		t.Errorf("FinalStatus() = %v, want %v (no tools used = trivial answer)", got, StatusVerified)
	}
}

func TestState_FinalStatus_ToolsWithoutVerificationIsPartial(t *testing.T) {
	st := New("fix the bug")
	st.RecordTool("shell", true)
	if got := st.FinalStatus(); got != StatusPartial {
		t.Errorf("FinalStatus() = %v, want %v (tools used, never verified)", got, StatusPartial)
	}
}

func TestState_FinalStatus_PassedVerificationIsVerified(t *testing.T) {
	st := New("fix the bug")
	st.RecordTool("shell", true)
	st.Apply(Patch{Verification: VerificationPassed, VerificationNote: "go test ./... passed"})
	if got := st.FinalStatus(); got != StatusVerified {
		t.Errorf("FinalStatus() = %v, want %v", got, StatusVerified)
	}
}

func TestState_FinalStatus_FailedVerificationIsFailed(t *testing.T) {
	st := New("fix the bug")
	st.RecordTool("shell", true)
	st.Apply(Patch{Verification: VerificationFailed})
	if got := st.FinalStatus(); got != StatusFailed {
		t.Errorf("FinalStatus() = %v, want %v", got, StatusFailed)
	}
}

func TestState_FinalStatus_OpenBlockerIsBlocked(t *testing.T) {
	st := New("fix the bug")
	st.RecordTool("shell", true)
	st.Apply(Patch{Blocker: "missing credentials for the staging DB"})
	if got := st.FinalStatus(); got != StatusBlocked {
		t.Errorf("FinalStatus() = %v, want %v", got, StatusBlocked)
	}
}

func TestState_RecordTool_TracksConsecutiveFailures(t *testing.T) {
	st := New("goal")
	st.RecordTool("shell", false)
	st.RecordTool("shell", false)
	if got := st.ConsecutiveFailures(); got != 2 {
		t.Fatalf("ConsecutiveFailures() = %d, want 2", got)
	}
	st.RecordTool("shell", true)
	if got := st.ConsecutiveFailures(); got != 0 {
		t.Fatalf("ConsecutiveFailures() after success = %d, want 0 (streak reset)", got)
	}
	if got := st.ToolCallCount(); got != 3 {
		t.Fatalf("ToolCallCount() = %d, want 3", got)
	}
}

func TestState_Summary_EmptyUntilSomethingRecorded(t *testing.T) {
	st := New("goal")
	if got := st.Summary(); got != "" {
		t.Errorf("Summary() on fresh state = %q, want empty", got)
	}

	st.Apply(Patch{Phase: "investigating"})
	if got := st.Summary(); got == "" {
		t.Errorf("Summary() after a patch = empty, want non-empty")
	}
}

func TestState_Summary_IncludesPlanAndBlockers(t *testing.T) {
	st := New("goal")
	st.Apply(Patch{
		Plan: []Step{
			{Text: "read the code", Status: StepDone},
			{Text: "add rate limiting", Status: StepInProgress},
		},
		Blocker: "flaky test in CI",
	})

	summary := st.Summary()
	for _, want := range []string{"read the code", "add rate limiting", "flaky test in CI"} {
		if !strings.Contains(summary, want) {
			t.Errorf("Summary() = %q, want it to contain %q", summary, want)
		}
	}
}

func TestState_Apply_PlanReplacesWholesale(t *testing.T) {
	st := New("goal")
	st.Apply(Patch{Plan: []Step{{Text: "a", Status: StepPending}}})
	st.Apply(Patch{Plan: []Step{{Text: "a", Status: StepDone}, {Text: "b", Status: StepPending}}})

	snap := st.Snapshot()
	if len(snap.Plan) != 2 {
		t.Fatalf("Plan length = %d, want 2", len(snap.Plan))
	}
	if snap.Plan[0].Status != StepDone {
		t.Errorf("Plan[0].Status = %v, want %v", snap.Plan[0].Status, StepDone)
	}
}

func TestState_Apply_DiscoveriesAccumulate(t *testing.T) {
	st := New("goal")
	st.Apply(Patch{Discovery: "middleware is registered in router.go"})
	st.Apply(Patch{Discovery: "tests live in router_test.go"})

	snap := st.Snapshot()
	if len(snap.Discoveries) != 2 {
		t.Fatalf("Discoveries length = %d, want 2", len(snap.Discoveries))
	}
}

func TestState_Apply_ClearBlockers(t *testing.T) {
	st := New("goal")
	st.Apply(Patch{Blocker: "blocked on X"})
	st.Apply(Patch{ClearBlockers: true})

	if got := st.Snapshot().Blockers; len(got) != 0 {
		t.Errorf("Blockers = %v, want empty after ClearBlockers", got)
	}
}

func TestState_SetStatus_OverridesFinalStatus(t *testing.T) {
	st := New("goal")
	st.SetStatus(StatusBlocked)
	if got := st.FinalStatus(); got != StatusBlocked {
		t.Errorf("FinalStatus() = %v, want %v after explicit SetStatus", got, StatusBlocked)
	}
}

func TestWithState_RoundTripsThroughContext(t *testing.T) {
	st := New("goal")
	ctx := WithState(context.Background(), st)

	got, ok := FromContext(ctx)
	if !ok || got != st {
		t.Fatalf("FromContext() = %v, %v, want the same *State back", got, ok)
	}

	if _, ok := FromContext(context.Background()); ok {
		t.Errorf("FromContext() on a plain context unexpectedly found a State")
	}
}
