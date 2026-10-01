package runtime

import (
	"context"
	"testing"
	"time"

	"forcefield/internal/tools/builtin"
	"forcefield/internal/tools/shell"
)

// Close terminates running background jobs through the shared registry:
// quitting (or a headless run ending) leaves no shell tree behind.
func TestCloseTerminatesBackgroundJobs(t *testing.T) {
	mgr, err := builtin.NewManager()
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	tool, ok := mgr.Lookup("shell_job")
	if !ok {
		t.Fatal("shell_job not registered")
	}
	sj, ok := tool.(*shell.ShellJob)
	if !ok || sj.Registry() == nil {
		t.Fatal("shell_job has no registry")
	}
	snap, err := sj.Registry().Start(context.Background(), "sleep 60", "", nil, 60*time.Second)
	if err != nil {
		t.Skipf("shell backend unavailable: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	rt := &Runtime{fullManager: mgr}
	done := make(chan error, 1)
	go func() { done <- rt.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Close hung with a running job")
	}

	got, err := sj.Registry().Poll(snap.ID)
	if err != nil {
		t.Fatalf("Poll after Close: %v", err)
	}
	if got.State != shell.JobCancelled {
		t.Errorf("state = %q, want cancelled", got.State)
	}
}

// Close stays nil-safe and idempotent for runtimes that never started
// background work.
func TestCloseNilSafe(t *testing.T) {
	var rt *Runtime
	if err := rt.Close(); err != nil {
		t.Errorf("nil Close = %v, want nil", err)
	}
	rt = &Runtime{}
	if err := rt.Close(); err != nil {
		t.Errorf("empty Close = %v, want nil", err)
	}
	if err := rt.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}
