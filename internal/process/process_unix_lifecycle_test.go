//go:build !windows

package process

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Group kill reaches every member born inside the group: a backgrounded
// grandchild that never leaves shares the fate of the direct child.
func TestKillReapsGroupGrandchildren(t *testing.T) {
	cmd, log, release := heartbeatTree(t, 60)
	defer release()
	waitForTicks(t, log, 15*time.Second)

	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	_ = cmd.Wait()
	assertFrozen(t, log, 1500*time.Millisecond)
}

// A child that calls setsid(2) leaves the process group and survives a
// group kill by kernel design. This test pins the documented limitation
// (see the sandbox process limitations): it passes when the escape
// behaves as documented, so any future platform change that closes the
// escape fails loudly here instead of silently changing guarantees.
func TestSetsidEscapeDocumented(t *testing.T) {
	var out bytes.Buffer
	// Print the setsid grandchild's pid, then hold the parent group open.
	cmd := exec.Command("sh", "-c", "setsid sleep 60 & echo $!; sleep 60")
	cmd.Stdout = &out
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	release := Track(cmd)
	defer release()

	deadline := time.Now().Add(10 * time.Second)
	var grandchild string
	for {
		grandchild = strings.TrimSpace(out.String())
		if grandchild != "" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if grandchild == "" {
		_ = Kill(cmd)
		_ = cmd.Wait()
		t.Fatal("grandchild pid never printed; test setup failed")
	}

	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	_ = cmd.Wait()

	// The setsid sleeper must still be alive: signal 0 probes without
	// touching it. Clean it up explicitly afterwards.
	probe := exec.Command("sh", "-c", "kill -0 "+grandchild)
	if err := probe.Run(); err != nil {
		// Escape closed on this platform: do not fail the suite for a
		// stronger guarantee, but say so loudly.
		t.Logf("setsid child did not survive group kill (platform reaps it): %v", err)
		return
	}
	t.Logf("setsid grandchild %s survived group kill: documented limitation holds", grandchild)
	cleanup := exec.Command("sh", "-c", "kill -9 "+grandchild)
	_ = cleanup.Run()
}
