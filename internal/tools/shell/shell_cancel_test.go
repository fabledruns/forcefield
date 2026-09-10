package shell

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"forcefield/internal/tools"
)

// TestShell_CancelReportsCancellation pins that a cancelled command
// returns a "cancelled" tool result (not a timeout or generic failure)
// on every platform.
func TestShell_CancelReportsCancellation(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan tools.Result, 1)
	go func() {
		result, _ := s.Execute(ctx, map[string]any{"command": cancellableCommand()})
		done <- result
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case result := <-done:
		if !result.IsError {
			t.Error("result.IsError = false, want true after cancellation")
		}
		if !strings.Contains(strings.ToLower(result.Content), "cancel") {
			t.Errorf("content = %q, want it to report cancellation", result.Content)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return within 15s of cancellation")
	}
}

// TestShell_CancelLeavesNoOrphanedDescendants pins orphan-process
// protection: a backgrounded grandchild writing heartbeats must stop
// after cancellation (POSIX process-group kill). Unix-only: on Windows
// the shell runs through the WSL relay, where a host temp path is not
// addressable from Bash — and Linux-side grandchildren outlive the
// relay by platform design (documented in internal/process), so no
// host-side kill primitive can assert on them. Windows-side tree
// coverage (relay promptness, job backstop, supervisor children) is
// pinned in internal/process with real Windows processes instead.
func TestShell_CancelLeavesNoOrphanedDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("orphan reaping inside the WSL distribution is not reachable from the host; Unix asserts no orphans")
	}
	requireShellBackend(t)
	s := NewShell()
	ctx, cancel := context.WithCancel(context.Background())

	heartbeat := filepath.Join(t.TempDir(), "heartbeat.log")
	// A detached grandchild ticking into a file, plus a foreground sleep
	// keeping the shell itself alive until it is killed.
	command := "( while true; do echo tick >> " + heartbeat + "; sleep 0.2; done & ) ; sleep 30"

	done := make(chan tools.Result, 1)
	go func() {
		result, _ := s.Execute(ctx, map[string]any{"command": command})
		done <- result
	}()

	time.Sleep(time.Second) // let the grandchild write several ticks
	cancel()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return within 15s of cancellation")
	}

	sizeAt := func() int64 {
		fi, err := os.Stat(heartbeat)
		if err != nil {
			return -1
		}
		return fi.Size()
	}
	if sizeAt() <= 0 {
		t.Fatalf("grandchild wrote no heartbeats before cancel; test setup failed")
	}
	time.Sleep(800 * time.Millisecond)
	before := sizeAt()
	time.Sleep(800 * time.Millisecond)
	if after := sizeAt(); after != before {
		t.Errorf("heartbeat file grew after cancellation (%d -> %d bytes): grandchild survived the group kill", before, after)
	}
}
