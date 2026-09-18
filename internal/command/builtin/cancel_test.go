package builtin

import (
	"strings"
	"testing"
)

// TestCancel_CancelsActiveRun is the smallest /cancel behavior: it asks the
// context to cancel the current run without erroring.
func TestCancel_CancelsActiveRun(t *testing.T) {
	ctx := &fakeContext{cancelActive: true}
	if err := NewCancel().Execute(ctx, nil); err != nil {
		t.Fatalf("Cancel.Execute returned an error: %v", err)
	}
	if !ctx.cancelCalled {
		t.Fatal("Cancel.Execute did not call ctx.CancelRun()")
	}
}

func TestCancel_IdleReportsNoActiveRun(t *testing.T) {
	ctx := &fakeContext{cancelActive: false}
	if err := NewCancel().Execute(ctx, nil); err != nil {
		t.Fatalf("Cancel.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "No active run") {
		t.Fatalf("/cancel output missing idle notice:\n%s", out)
	}
}

func TestCancel_RejectsArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewCancel().Execute(ctx, []string{"now"}); err == nil {
		t.Fatal("Cancel.Execute accepted unexpected arguments")
	}
}
