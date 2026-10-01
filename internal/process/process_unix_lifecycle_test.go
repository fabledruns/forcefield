//go:build !windows

package process

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// A child that calls setsid(2) leaves the process group and survives a
// group kill by kernel design. This test proves the escape by effect:
// the setsid grandchild keeps ticking after its whole original group
// is killed. Effect-based (not pid-based: $! names the setsid wrapper,
// which exits immediately, not the sleep it spawns), file-synchronized
// (no shared buffers, race-clean), with explicit cleanup via a unique
// pkill marker. It passes when the escape behaves as documented, so any
// platform change that closes the escape fails loudly here instead of
// silently changing guarantees.
func TestSetsidEscapeDocumented(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skipf("setsid unavailable on this platform: %v", err)
	}
	if _, err := exec.LookPath("pkill"); err != nil {
		t.Skipf("pkill unavailable, cannot guarantee cleanup: %v", err)
	}
	log := t.TempDir() + "/setsid-escape-heartbeat.log"
	marker := "ff-setsid-escape-probe"
	// The ticker carries a unique marker in its command line so cleanup
	// can target exactly this grandchild and nothing else.
	script := "setsid sh -c 'echo " + marker + " started; while true; do echo tick >> " + log + "; sleep 0.2; done' & sleep 60"
	cmd := exec.Command("sh", "-c", script)
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	release := Track(cmd)
	defer release()

	waitForPath(t, log, 30*time.Second)

	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	_ = cmd.Wait()

	// The escaped ticker must still be alive: the log keeps growing
	// after its entire original group died.
	before := fileSize(t, log)
	time.Sleep(1500 * time.Millisecond)
	after := fileSize(t, log)
	if after <= before {
		t.Fatalf("setsid grandchild stopped ticking after group kill (%d -> %d bytes): escape not observed", before, after)
	}
	t.Logf("setsid grandchild survived group kill (log %d -> %d bytes): documented limitation holds", before, after)

	cleanup := exec.Command("pkill", "-9", "-f", marker)
	_ = cleanup.Run()
	waitForQuiet(t, log)
}

// waitForPath blocks until path exists and is non-empty.
func waitForPath(t *testing.T, log string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if fileSize(t, log) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("escaped ticker wrote no heartbeats; test setup failed")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func fileSize(t *testing.T, log string) int64 {
	t.Helper()
	fi, err := os.Stat(log)
	if err != nil {
		return -1
	}
	return fi.Size()
}

// waitForQuiet blocks until the ticker stops after cleanup, so no
// orphaned writer survives the test on any outcome.
func waitForQuiet(t *testing.T, log string) {
	t.Helper()
	before := fileSize(t, log)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		after := fileSize(t, log)
		if after == before {
			return
		}
		before = after
	}
	t.Error("escaped ticker kept writing 15s after pkill cleanup")
}
