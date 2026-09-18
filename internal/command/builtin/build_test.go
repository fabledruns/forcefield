package builtin

import (
	"errors"
	"testing"
)

// TestBuild_StartsBuild is the smallest /build behavior: it asks the
// context to execute the accepted plan.
func TestBuild_StartsBuild(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewBuild().Execute(ctx, nil); err != nil {
		t.Fatalf("Build.Execute returned an error: %v", err)
	}
	if ctx.buildCalls != 1 {
		t.Fatalf("StartBuild calls = %d, want 1", ctx.buildCalls)
	}
}

// TestBuild_PropagatesNoPlanError keeps the no-plan failure visible: with
// no accepted plan, the context reports it and /build does not start a run.
func TestBuild_PropagatesNoPlanError(t *testing.T) {
	ctx := &fakeContext{buildErr: errors.New("No plan yet — run /plan <task> first.")}
	if err := NewBuild().Execute(ctx, nil); err == nil {
		t.Fatal("Build.Execute swallowed the no-plan error")
	}
	if ctx.buildCalls != 1 {
		t.Fatalf("StartBuild calls = %d, want 1", ctx.buildCalls)
	}
}
