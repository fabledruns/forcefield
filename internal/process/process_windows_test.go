//go:build windows

package process

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requirePowerShell skips the test when powershell.exe is unavailable.
// It ships with Windows; the skip is belt-and-braces for stripped images.
func requirePowerShell(t *testing.T) string {
	t.Helper()
	exe, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skipf("powershell.exe unavailable; skipping Windows tree test: %v", err)
	}
	return exe
}

// heartbeatTree writes parent/child scripts that tick a file from a
// detached grandchild, then starts the parent under the caller's
// lifecycle. Script files dodge every layer of shell quoting: argv
// stays plain paths.
func heartbeatTree(t *testing.T, holdSeconds int) (cmd *exec.Cmd, log string) {
	t.Helper()
	exe := requirePowerShell(t)
	dir := t.TempDir()
	log = filepath.Join(dir, "heartbeat.log")
	child := filepath.Join(dir, "child.ps1")
	parent := filepath.Join(dir, "parent.ps1")
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(child, "while($true){ Add-Content '"+log+"' 'tick'; Start-Sleep -Milliseconds 200 }\n")
	write(parent, "Start-Process powershell -ArgumentList '-NoProfile','-NonInteractive','-File','"+child+"' -WindowStyle Hidden\n"+
		"Start-Sleep -Seconds "+strconv.Itoa(holdSeconds)+"\n")
	cmd = exec.Command(exe, "-NoProfile", "-NonInteractive", "-File", parent)
	return cmd, log
}

// waitForTicks blocks until the heartbeat file has ticks or the timeout
// hits, so slow CI machines don't flake on fixed sleeps.
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

// assertFrozenFailsIfGrowing fails when the heartbeat file grows over
// the window: the descendant survived its tree's death.
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

func TestNormalCompletionUnaffected(t *testing.T) {
	exe := requirePowerShell(t)
	var out bytes.Buffer
	code, err := Run(nil, exe,
		[]string{"-NoProfile", "-NonInteractive", "-Command", "Write-Output hello"},
		&out, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("stdout = %q, want hello", out.String())
	}
}

func TestKillTerminatesTree(t *testing.T) {
	cmd, log := heartbeatTree(t, 60)
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	release := Track(cmd)
	defer release()
	waitForTicks(t, log, 15*time.Second)

	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Log("child reaped without error after tree kill (already gone)")
	}
	assertFrozen(t, log, 1500*time.Millisecond)
}

func TestJobCloseKillsTreeWithoutKill(t *testing.T) {
	// Crash coverage: releasing the job handle with the tree still alive
	// must reap it via KILL_ON_JOB_CLOSE, with no Kill call at all. This
	// is what happens when the Forcefield process itself dies outright.
	cmd, log := heartbeatTree(t, 60)
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	release := Track(cmd)
	waitForTicks(t, log, 15*time.Second)

	release() // no Kill: simulate harness death closing the handle
	_ = cmd.Wait()
	assertFrozen(t, log, 1500*time.Millisecond)
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
