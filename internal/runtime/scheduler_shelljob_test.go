package runtime

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
	shellpkg "forcefield/internal/tools/shell"
)

// TestHelperBlockForever is the child process for the stub executor below.
// The parent kills it; it never exits on its own. It sleeps instead of
// blocking on select{} so the Go runtime's deadlock detector does not
// kill the child before the parent does.
func TestHelperBlockForever(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	time.Sleep(60 * time.Second)
}

// stubJobExecutor builds a controllable long-running process without a
// shell backend, so the test is deterministic on every platform. It
// honours the context it receives via exec.CommandContext exactly like
// the real executors, which is what makes this a faithful reproduction:
// on the old code that context is the scheduler's per-attempt context
// (cancelled right after Start returns); on the fixed code it is
// detached via context.WithoutCancel.
type stubJobExecutor struct {
	dir         string
	gotCtxAfter atomic.Value // stores context.Context seen by Prepare
}

func (s *stubJobExecutor) Prepare(ctx context.Context, req sandbox.Request) (*sandbox.Prepared, error) {
	s.gotCtxAfter.Store(ctx)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperBlockForever")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	dir := s.dir
	if dir == "" {
		dir = os.TempDir()
	}
	return &sandbox.Prepared{Cmd: cmd, Dir: dir}, nil
}

func (s *stubJobExecutor) Probe(context.Context) error { return nil }

func (s *stubJobExecutor) Describe(context.Context) sandbox.Enforcement {
	return sandbox.Enforcement{Mode: sandbox.ModeNative}
}

// TestScheduler_ShellJobStartSurvivesPerAttemptCancel runs a real
// shell_job start through the real scheduler path. The scheduler wraps
// every attempt in a timeout context it cancels as soon as execute()
// returns; the background job must survive that cancellation and die
// only on explicit run cancellation.
func TestScheduler_ShellJobStartSurvivesPerAttemptCancel(t *testing.T) {
	stub := &stubJobExecutor{dir: t.TempDir()}
	jobTool := shellpkg.NewShellJobWithExecutor(stub)
	manager := newTestManager(t, jobTool)
	s := newScheduler(manager, nil, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	control := tools.NewRunControl(runCtx)
	runCtx = tools.WithRunControl(runCtx, control)

	calls := []providers.ToolCall{{
		ID:   "call-1",
		Name: "shell_job",
		Arguments: map[string]any{
			"action":          "start",
			"command":         "ignored-by-stub",
			"timeout_seconds": float64(60),
		},
	}}
	results := s.runWithConcurrency(runCtx, calls, func(Event) bool { return true }, manager, 1)
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if !results[0].Success {
		t.Fatalf("start result = %+v, want success (content=%q err=%v)", results[0], results[0].Content, results[0].Err)
	}

	// The scheduler's per-attempt context is cancelled by the time
	// runWithConcurrency returns (it calls cancel() after execute).
	// The captured Prepare context proves detachment deterministically:
	// on the old code it is already cancelled here.
	raw := stub.gotCtxAfter.Load()
	if raw == nil {
		t.Fatal("stub executor never saw Prepare")
	}
	if err := raw.(context.Context).Err(); err != nil {
		t.Fatalf("Prepare context Err() = %v after attempt return, want nil (job must be detached from the per-attempt context)", err)
	}

	// Behavioural proof: the job is still alive well after the attempt
	// context was cancelled. The 500ms window lets the old code's
	// SIGKILL + async reaper settle so the test fails reliably there.
	// (Timing is unavoidable: the kill/reap it guards against is itself
	// asynchronous.)
	time.Sleep(500 * time.Millisecond)
	snap, err := jobTool.Registry().Poll("job-1")
	if err != nil {
		t.Fatalf("Poll(job-1): %v", err)
	}
	if string(snap.State) != "running" {
		t.Fatalf("job state = %q after per-attempt cancel, want running (output=%q note=%q)", snap.State, snap.Output, snap.Note)
	}

	// Explicit run cancellation must still terminate the job via the
	// preserved RunControl watcher.
	cancelRun()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, err = jobTool.Registry().Poll("job-1")
		if err != nil {
			t.Fatalf("Poll(job-1) after run cancel: %v", err)
		}
		if string(snap.State) != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still running 5s after run cancellation, want terminated (RunControl watcher must still work)")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(snap.State) != "cancelled" {
		t.Fatalf("job state after run cancel = %q, want cancelled", snap.State)
	}
	if !strings.Contains(snap.Note, "cancelled with agent run") {
		t.Errorf("job note = %q, want RunControl cancellation note", snap.Note)
	}
}
