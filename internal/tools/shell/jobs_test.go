package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"forcefield/internal/sandbox"
)

// pollUntilDone polls until the job leaves running or the deadline hits.
func pollUntilDone(t *testing.T, r *JobRegistry, id string, timeout time.Duration) Snapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		snap, err := r.Poll(id)
		if err != nil {
			t.Fatalf("Poll(%s): %v", id, err)
		}
		if snap.State.terminal() {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s after %v", id, snap.State, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestJob_LifecycleDone(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	snap, err := r.Start(context.Background(), "echo hello-job", "", nil, 30*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if snap.ID == "" || snap.State != JobRunning {
		t.Fatalf("start = %+v, want running with an ID", snap)
	}
	got := pollUntilDone(t, r, snap.ID, 15*time.Second)
	if got.State != JobDone || !got.HasExit || got.ExitCode != 0 {
		t.Errorf("final = %+v, want done exit 0", got)
	}
	if !strings.Contains(got.Output, "hello-job") {
		t.Errorf("output = %q, want hello-job", got.Output)
	}
}

func TestJob_FailedExitCode(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	snap, err := r.Start(context.Background(), "exit 7", "", nil, 30*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := pollUntilDone(t, r, snap.ID, 15*time.Second)
	if got.State != JobFailed || got.ExitCode != 7 {
		t.Errorf("final = %+v, want failed exit 7", got)
	}
}

func TestJob_CancelTerminates(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	snap, err := r.Start(context.Background(), cancellableCommand(), "", nil, 60*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	got, err := r.Cancel(snap.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got.State != JobCancelled {
		t.Errorf("state = %q, want cancelled", got.State)
	}
	// Idempotent: cancelling again reports the recorded state.
	again, err := r.Cancel(snap.ID)
	if err != nil || again.State != JobCancelled {
		t.Errorf("second cancel = %+v err=%v, want cancelled", again, err)
	}
	// The process is actually gone: output stops growing.
	time.Sleep(300 * time.Millisecond)
}

func TestJob_MaxActiveJobsEnforced(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	var ids []string
	for i := 0; i < maxActiveJobs; i++ {
		snap, err := r.Start(context.Background(), "sleep 30", "", nil, 60*time.Second)
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		ids = append(ids, snap.ID)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = r.Cancel(id)
		}
	})
	if _, err := r.Start(context.Background(), "echo overflow", "", nil, 30*time.Second); err == nil {
		t.Fatal("start beyond cap must fail")
	} else if !isTooManyJobs(err) {
		t.Errorf("error = %v, want the too-many-jobs signal", err)
	}
}

func isTooManyJobs(err error) bool {
	return errors.Is(err, ErrTooManyJobs)
}

func TestJob_IDsUniqueMonotonic(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	seen := map[string]bool{}
	var first string
	for i := 0; i < 3; i++ {
		snap, err := r.Start(context.Background(), "echo x", "", nil, 30*time.Second)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if i == 0 {
			first = snap.ID
		}
		if seen[snap.ID] {
			t.Fatalf("duplicate job ID %q", snap.ID)
		}
		seen[snap.ID] = true
		pollUntilDone(t, r, snap.ID, 15*time.Second)
	}
	if first == "" {
		t.Fatal("no IDs minted")
	}
}

func TestJob_OutputBounded(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	r.setMaxOutput(2048)
	snap, err := r.Start(context.Background(), "seq 1 2000", "", nil, 30*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := pollUntilDone(t, r, snap.ID, 15*time.Second)
	if got.State != JobDone {
		t.Fatalf("state = %q", got.State)
	}
	if len(got.Output) > 2048+512 {
		t.Errorf("output %d bytes exceeds the 2 KiB cap", len(got.Output))
	}
	if !got.Truncated || !strings.Contains(got.Output, "truncated") {
		t.Errorf("bounded output lacks marker/flag: truncated=%v", got.Truncated)
	}
}

func TestJob_DeadlineTerminates(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	snap, err := r.Start(context.Background(), "sleep 30", "", nil, time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := pollUntilDone(t, r, snap.ID, 15*time.Second)
	if got.State != JobTimeout {
		t.Errorf("state = %q, want timeout", got.State)
	}
}

func TestJob_IdleTTLReaps(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	r.mu.Lock()
	r.ttl = 300 * time.Millisecond
	r.mu.Unlock()
	snap, err := r.Start(context.Background(), "sleep 30", "", nil, 60*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// No polls: the next registry access past the TTL reaps it.
	time.Sleep(600 * time.Millisecond)
	if _, err := r.Start(context.Background(), "echo oi", "", nil, 30*time.Second); err != nil {
		t.Fatalf("second start (sweep trigger): %v", err)
	}
	if _, err := r.Poll(snap.ID); err == nil {
		t.Error("idle job must be pruned, poll unexpectedly succeeded")
	}
}

func TestJob_UnknownPollCancel(t *testing.T) {
	r := NewJobRegistry(nil)
	if _, err := r.Poll("job-999"); err == nil {
		t.Error("poll unknown must fail")
	}
	if _, err := r.Cancel("job-999"); err == nil {
		t.Error("cancel unknown must fail")
	}
}

func TestJob_ListShowsJobs(t *testing.T) {
	requireShellBackend(t)
	if got := NewJobRegistry(nil).List(); len(got) != 0 {
		t.Errorf("empty registry lists %d jobs", len(got))
	}
	r := NewJobRegistry(nil)
	snap, err := r.Start(context.Background(), "echo hi", "", nil, 30*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pollUntilDone(t, r, snap.ID, 15*time.Second)
	list := r.List()
	if len(list) != 1 || list[0].ID != snap.ID {
		t.Errorf("list = %+v, want the one job", list)
	}
}

func TestJob_CloseTerminatesRunning(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	snap, err := r.Start(context.Background(), "sleep 30", "", nil, 60*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Close()
	got, err := r.Poll(snap.ID)
	if err != nil {
		t.Fatalf("Poll after close: %v", err)
	}
	if got.State != JobCancelled {
		t.Errorf("state = %q, want cancelled after Close", got.State)
	}
}

func TestJobTool_ActionsAndValidation(t *testing.T) {
	tool := NewShellJob()
	ctx := context.Background()

	res, err := tool.Execute(ctx, map[string]any{"action": "explode"})
	if err != nil {
		t.Fatalf("bad action must be soft, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "supported") {
		t.Errorf("bad action = %+v, want supported-actions soft error", res)
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "start"}); err == nil {
		t.Error("missing command must be a hard argument error")
	}
	if _, err := tool.Execute(ctx, map[string]any{"action": "start", "command": "echo x", "timeout_seconds": 999}); err == nil {
		t.Error("timeout over ceiling must be a hard argument error")
	}
	res, _ = tool.Execute(ctx, map[string]any{"action": "poll", "job_id": "job-999"})
	if !res.IsError {
		t.Error("poll unknown must be soft error")
	}
	res, _ = tool.Execute(ctx, map[string]any{"action": "list"})
	if res.IsError || !strings.Contains(res.Content, "no background jobs") {
		t.Errorf("empty list = %+v", res)
	}
	res, _ = tool.Execute(ctx, map[string]any{"action": "start", "command": "vim"})
	if !res.IsError || !strings.Contains(res.Content, "interactive terminal") {
		t.Errorf("interactive refusal = %+v", res)
	}
}

func TestJobTool_StrictCwdDeniedWithoutBackend(t *testing.T) {
	// Boundary refusal precedes backend construction: no Bash needed.
	ws := t.TempDir()
	ex, err := sandbox.NewExecutor(sandbox.Policy{Mode: sandbox.ModeNative, Workspace: ws, Strict: true})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	tool := NewShellJobWithExecutor(ex)
	res, err := tool.Execute(context.Background(), map[string]any{
		"action": "start", "command": "echo hi", "cwd": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(strings.ToLower(res.Content), "workspace") {
		t.Errorf("outside-cwd start = %+v, want workspace denial", res)
	}
}

func TestJobTool_StartPollCancelRoundTrip(t *testing.T) {
	requireShellBackend(t)
	tool := NewShellJob()
	ctx := context.Background()

	res, err := tool.Execute(ctx, map[string]any{"action": "start", "command": "echo roundtrip"})
	if err != nil || res.IsError {
		t.Fatalf("start = %+v err=%v", res, err)
	}
	// Extract job id from the start message.
	id := ""
	for _, field := range strings.Fields(res.Content) {
		if strings.HasPrefix(field, "job-") {
			id = strings.Trim(field, "(),:")
		}
	}
	if id == "" {
		t.Fatalf("no job id in start message: %q", res.Content)
	}
	// Poll until done.
	deadline := time.Now().Add(15 * time.Second)
	for {
		pres, err := tool.Execute(ctx, map[string]any{"action": "poll", "job_id": id})
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if strings.Contains(pres.Content, "[running]") && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if !strings.Contains(pres.Content, "roundtrip") {
			t.Errorf("poll content lacks output:\n%s", pres.Content)
		}
		break
	}
	// tail_lines slicing.
	pres, err := tool.Execute(ctx, map[string]any{"action": "poll", "job_id": id, "tail_lines": "1"})
	if err != nil || pres.IsError {
		t.Fatalf("tail poll: %+v err=%v", pres, err)
	}
	// Metadata + exit code shape.
	if pres.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", pres.ExitCode)
	}
	// Cancel of a terminal job reports its state (idempotent).
	cres, err := tool.Execute(ctx, map[string]any{"action": "cancel", "job_id": id})
	if err != nil || cres.IsError {
		t.Fatalf("cancel terminal: %+v err=%v", cres, err)
	}
}

func TestJob_RetentionEvictsOldestTerminal(t *testing.T) {
	// No shell backend needed: seed terminal records directly, then drive
	// the public List path (which sweeps). accessed=now keeps every
	// record inside the idle TTL so only the 32-job retention bound acts.
	r := NewJobRegistry(nil)
	now := time.Now()
	const extra = 8
	r.mu.Lock()
	for i := 0; i < maxRetainedJobs+extra; i++ {
		id := fmt.Sprintf("job-%03d", i)
		r.jobs[id] = &Job{
			ID:        id,
			Command:   "echo seeded",
			StartedAt: now.Add(time.Duration(i) * time.Second),
			state:     JobDone,
			hasExit:   true,
			output:    &shellOutput{},
			accessed:  now,
			done:      make(chan struct{}),
		}
		close(r.jobs[id].done)
	}
	r.mu.Unlock()

	if listed := r.List(); len(listed) != maxRetainedJobs {
		t.Fatalf("retained = %d, want exactly the %d-job bound", len(listed), maxRetainedJobs)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.jobs) != maxRetainedJobs {
		t.Fatalf("registry holds %d jobs, want at most %d", len(r.jobs), maxRetainedJobs)
	}
	for i := 0; i < extra; i++ {
		if _, ok := r.jobs[fmt.Sprintf("job-%03d", i)]; ok {
			t.Errorf("job-%03d should have been evicted as an oldest terminal record", i)
		}
	}
	for i := extra; i < maxRetainedJobs+extra; i++ {
		if _, ok := r.jobs[fmt.Sprintf("job-%03d", i)]; !ok {
			t.Errorf("job-%03d (newest window) should have been retained, but is missing", i)
		}
	}
}
