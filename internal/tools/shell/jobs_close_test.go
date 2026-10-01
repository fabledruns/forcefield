package shell

import (
	"context"
	"testing"
	"time"
)

// Close terminates running jobs and leaves terminal records readable:
// the shutdown path reaps everything without polling first.
func TestRegistryCloseTerminatesRunning(t *testing.T) {
	requireShellBackend(t)
	r := NewJobRegistry(nil)
	snap, err := r.Start(context.Background(), cancellableCommand(), "", nil, 60*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Close()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Close hung with a running job")
	}

	got, err := r.Poll(snap.ID)
	if err != nil {
		t.Fatalf("Poll after Close: %v", err)
	}
	if got.State != JobCancelled {
		t.Errorf("state = %q, want cancelled", got.State)
	}

	// The reaper finished: Wait returned and both pipes drained, so no
	// descendant holds the job open past shutdown.
	r.mu.Lock()
	j := r.jobs[snap.ID]
	r.mu.Unlock()
	if j == nil {
		t.Fatal("job record vanished on Close; terminal records must stay readable")
	}
	select {
	case <-j.reaped:
	case <-time.After(15 * time.Second):
		t.Fatal("reaper never finished after Close")
	}
}
