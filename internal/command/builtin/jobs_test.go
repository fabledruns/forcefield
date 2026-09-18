package builtin

import (
	"strings"
	"testing"

	"forcefield/internal/command"
)

// TestJobs_EmptyReportsNone is the smallest /jobs behavior: with no
// background jobs it says so without invoking the model.
func TestJobs_EmptyReportsNone(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewJobs().Execute(ctx, nil); err != nil {
		t.Fatalf("Jobs.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "No background jobs") {
		t.Fatalf("/jobs output missing empty notice:\n%s", out)
	}
}

func TestJobs_ListsJobs(t *testing.T) {
	ctx := &fakeContext{jobs: []command.JobSnapshot{
		{ID: "job-1", Command: "sleep 30", State: "running"},
		{ID: "job-2", Command: "go test ./...", State: "done", HasExit: true, ExitCode: 0},
	}}
	if err := NewJobs().Execute(ctx, nil); err != nil {
		t.Fatalf("Jobs.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	for _, want := range []string{"job-1", "running", "sleep 30", "job-2", "done", "exit 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("/jobs output missing %q:\n%s", want, out)
		}
	}
}

func TestJobs_TruncatesLongCommands(t *testing.T) {
	long := strings.Repeat("x", 200)
	ctx := &fakeContext{jobs: []command.JobSnapshot{{ID: "job-1", Command: long, State: "running"}}}
	if err := NewJobs().Execute(ctx, nil); err != nil {
		t.Fatalf("Jobs.Execute returned an error: %v", err)
	}
	if out := strings.Join(ctx.lines, "\n"); strings.Contains(out, long) {
		t.Fatalf("/jobs output did not truncate a long command:\n%s", out)
	}
}

func TestJobs_RejectsArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewJobs().Execute(ctx, []string{"job-1"}); err == nil {
		t.Fatal("Jobs.Execute accepted unexpected arguments")
	}
}
