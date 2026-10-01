//go:build windows

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Configure must mark the child suspended so Track can assign the job
// before any child code runs. This white-box pin is the deterministic
// half of the Start→Track race closure; the heartbeat tests below are
// the behavioral half.
func TestConfigureStartsSuspended(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 0")
	Configure(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("Configure left SysProcAttr nil")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_SUSPENDED == 0 {
		t.Error("Configure did not set CREATE_SUSPENDED: the Start→Track race is open")
	}
}

// delayedHeartbeatTree starts a parent that spawns its heartbeat
// grandchild only after delaySeconds: the descendant is necessarily
// born after Track, exercising job inheritance for late-born children.
// (The immediate variant in TestKillTerminatesTree covers children born
// at startup, which the suspended start cages by construction.)
func delayedHeartbeatTree(t *testing.T, delaySeconds, holdSeconds int) (*exec.Cmd, string) {
	t.Helper()
	exe := requirePowerShell(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "heartbeat.log")
	child := filepath.Join(dir, "child.ps1")
	parent := filepath.Join(dir, "parent.ps1")
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(child, "while($true){ Add-Content '"+log+"' 'tick'; Start-Sleep -Milliseconds 200 }\n")
	write(parent, "Start-Sleep -Seconds "+strconv.Itoa(delaySeconds)+"\n"+
		"Start-Process powershell -ArgumentList '-NoProfile','-NonInteractive','-File','"+child+"' -WindowStyle Hidden\n"+
		"Start-Sleep -Seconds "+strconv.Itoa(holdSeconds)+"\n")
	cmd := exec.Command(exe, "-NoProfile", "-NonInteractive", "-File", parent)
	return cmd, log
}

func TestKillReapsDelayedGrandchild(t *testing.T) {
	cmd, log := delayedHeartbeatTree(t, 2, 60)
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	release := Track(cmd)
	defer release()
	// Generous setup budget: two nested cold powershell starts on a
	// loaded CI runner can take an order of magnitude longer than
	// locally. Only the setup waits; the kill/frozen assertions below
	// stay strict.
	waitForTicks(t, log, 60*time.Second)

	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Log("child reaped without error after tree kill (already gone)")
	}
	assertFrozen(t, log, 1500*time.Millisecond)
}
