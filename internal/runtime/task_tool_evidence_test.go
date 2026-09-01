package runtime

import (
	"context"
	"testing"

	"forcefield/internal/task"
)

func TestUpdateTaskState_VerificationPassedRequiresNote(t *testing.T) {
	st := task.New("goal")
	ctx := task.WithState(context.Background(), st)
	tool := newUpdateTaskStateTool()
	res, execErr := tool.Execute(ctx, map[string]any{"verification": "passed"})
	if execErr != nil {
		t.Fatalf("Execute returned Go error: %v", execErr)
	}
	if !res.IsError {
		t.Fatal("verification passed without note should be rejected as IsError")
	}
	if st.Snapshot().Verification == task.VerificationPassed {
		t.Error("state should not have been updated to passed without evidence")
	}
}

func TestUpdateTaskState_VerificationPassedWithNoteSucceeds(t *testing.T) {
	st := task.New("goal")
	ctx := task.WithState(context.Background(), st)
	tool := newUpdateTaskStateTool()
	res, err := tool.Execute(ctx, map[string]any{"verification": "passed", "verification_note": "go test ./... passed"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got IsError with %q", res.Content)
	}
	if st.Snapshot().Verification != task.VerificationPassed {
		t.Error("verification should be passed")
	}
}

func TestUpdateTaskState_StatusVerifiedRequiresPassed(t *testing.T) {
	st := task.New("goal")
	ctx := task.WithState(context.Background(), st)
	tool := newUpdateTaskStateTool()
	res, _ := tool.Execute(ctx, map[string]any{"status": "verified"})
	if !res.IsError {
		t.Fatal("status verified without verification passed should be rejected")
	}
	// Now with verification passed in same call, should succeed
	res, _ = tool.Execute(ctx, map[string]any{"status": "verified", "verification": "passed", "verification_note": "evidence"})
	if res.IsError {
		t.Fatalf("status verified with verification passed should succeed, got %q", res.Content)
	}
}

func TestUpdateTaskState_UnknownFieldRejected(t *testing.T) {
	st := task.New("goal")
	ctx := task.WithState(context.Background(), st)
	tool := newUpdateTaskStateTool()
	res, _ := tool.Execute(ctx, map[string]any{"unknown_field": "x"})
	if !res.IsError {
		t.Fatal("unknown field should be rejected")
	}
}

func TestUpdateTaskState_WrongTypeRejected(t *testing.T) {
	st := task.New("goal")
	ctx := task.WithState(context.Background(), st)
	tool := newUpdateTaskStateTool()
	res, _ := tool.Execute(ctx, map[string]any{"verification": 123})
	if !res.IsError {
		t.Fatal("wrong type for verification should be rejected")
	}
	res, _ = tool.Execute(ctx, map[string]any{"clear_blockers": "yes"})
	if !res.IsError {
		t.Fatal("wrong type for clear_blockers should be rejected")
	}
}
