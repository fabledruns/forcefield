package builtin

import (
	"errors"
	"testing"
)

// TestPlan_RequestsPlanTurn is the smallest /plan behavior: the task is
// handed to the context for a planning turn without modifying anything.
func TestPlan_RequestsPlanTurn(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewPlan().Execute(ctx, []string{"add", "a", "flag"}); err != nil {
		t.Fatalf("Plan.Execute returned an error: %v", err)
	}
	if ctx.planTask != "add a flag" {
		t.Fatalf("StartPlan(task) = %q, want %q", ctx.planTask, "add a flag")
	}
}

func TestPlan_RejectsEmptyTask(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewPlan().Execute(ctx, nil); err == nil {
		t.Fatal("Plan.Execute accepted an empty task")
	}
	if ctx.planTask != "" {
		t.Fatalf("StartPlan was called with %q for an empty task", ctx.planTask)
	}
}

func TestPlan_PropagatesStartError(t *testing.T) {
	ctx := &fakeContext{planErr: errors.New("busy")}
	if err := NewPlan().Execute(ctx, []string{"something"}); err == nil {
		t.Fatal("Plan.Execute swallowed the StartPlan error")
	}
}
