//go:build !windows

package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// heartbeatTree starts a shell whose detached grandchild ticks a file,
// returning the cmd (configured, started, tracked) for the caller to
// kill. Mirrors the shell tool's orphan test at package level.
func heartbeatTree(t *testing.T, holdSeconds int) (*exec.Cmd, string, ReleaseFunc) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "heartbeat.log")
	script := "( while true; do echo tick >> " + log + "; sleep 0.2; done & ) ; sleep " + itoa(holdSeconds)
	cmd := exec.Command("sh", "-c", script)
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return cmd, log, Track(cmd)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

func waitForTicks(t *testing.T, log string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if fi, err := os.Stat(log); err == nil && fi.Size() > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("grandchild wrote no heartbeats; test setup failed")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func assertFrozen(t *testing.T, log string, window time.Duration) {
	t.Helper()
	before, err := os.Stat(log)
	if err != nil {
		t.Fatalf("stat heartbeat: %v", err)
	}
	time.Sleep(window)
	after, err := os.Stat(log)
	if err != nil {
		t.Fatalf("stat heartbeat: %v", err)
	}
	if after.Size() != before.Size() {
		t.Errorf("heartbeat grew after tree death (%d -> %d bytes): descendant survived", before.Size(), after.Size())
	}
}

func TestKillTerminatesTree(t *testing.T) {
	cmd, log, release := heartbeatTree(t, 60)
	defer release()
	waitForTicks(t, log, 15*time.Second)

	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	_ = cmd.Wait()
	assertFrozen(t, log, 1500*time.Millisecond)
}

func TestTerminateIsGraceful(t *testing.T) {
	// A plain sleeper dies on SIGTERM: termination is prompt and needs
	// no escalation.
	cmd := exec.Command("sh", "-c", "sleep 60")
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	release := Track(cmd)
	defer release()

	if err := Terminate(cmd); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("process ignored SIGTERM for 10s")
	}
}

func TestRunCancelKillsDescendants(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "heartbeat.log")
	script := "( while true; do echo tick >> " + log + "; sleep 0.2; done & ) ; sleep 60"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 1)
	go func() {
		_, _ = Run(ctx, "sh", []string{"-c", script}, nil, nil, nil)
		done <- struct{}{}
	}()

	waitForTicks(t, log, 15*time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	assertFrozen(t, log, 1500*time.Millisecond)
}

func TestRunEscalatesAfterGrace(t *testing.T) {
	old := terminateGracePeriod
	terminateGracePeriod = 300 * time.Millisecond
	defer func() { terminateGracePeriod = old }()

	// Ignores SIGTERM: Run must escalate to Kill after the grace period
	// instead of waiting forever.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 1)
	go func() {
		defer close(done)
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	code, err := Run(ctx, "sh",
		[]string{"-c", "trap '' TERM; sleep 60"}, nil, nil, nil)
	<-done
	_ = code
	_ = err
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("Run took %s with 300ms grace; escalation did not fire", elapsed)
	}
	// Outcome shape (killed, no code) is asserted by the exitCode unit
	// test; here only prompt termination matters.
}

func TestNilSafety(t *testing.T) {
	Configure(nil)
	Track(nil)()
	if err := Kill(nil); err != nil {
		t.Errorf("Kill(nil) = %v, want nil", err)
	}
	if err := Terminate(nil); err != nil {
		t.Errorf("Terminate(nil) = %v, want nil", err)
	}
}
