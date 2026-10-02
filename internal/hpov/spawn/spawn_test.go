package spawn

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// TestMain re-execs this test binary as a scripted child when
// HPOV_TEST_CHILD=1: __exit N | __sleep-ms N | __echo-out MSG.
func TestMain(m *testing.M) {
	if os.Getenv("HPOV_TEST_CHILD") != "1" {
		os.Exit(m.Run())
	}
	mode := os.Getenv("HPOV_TEST_MODE")
	switch {
	case mode == "__echo-out":
		_, _ = os.Stdout.WriteString(os.Getenv("HPOV_TEST_MSG"))
	case len(mode) > len("__exit ")-1 && mode[:7] == "__exit ":
		n, _ := strconv.Atoi(mode[7:])
		os.Exit(n)
	case len(mode) > len("__sleep-ms ")-1 && mode[:11] == "__sleep-ms ":
		ms, _ := strconv.Atoi(mode[11:])
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	os.Exit(0)
}

func childEnv(mode string) []string {
	return []string{"HPOV_TEST_CHILD=1", "HPOV_TEST_MODE=" + mode, "PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
}

func self(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExitCode(t *testing.T) {
	res, err := Run(context.Background(), Options{
		Path: self(t), Env: childEnv("__exit 3"),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d, want 3", res.ExitCode)
	}
	if res.WallMS <= 0 || res.WallMS > 30000 {
		t.Fatalf("wall %v out of range", res.WallMS)
	}
	if res.UserMS < 0 || res.SysMS < 0 {
		t.Fatalf("negative cpu times %v %v", res.UserMS, res.SysMS)
	}
}

func TestWallOrdering(t *testing.T) {
	// A 300 ms sleeper must measure >= ~250 ms (generous floor for
	// loaded CI) and far below the timeout.
	res, err := Run(context.Background(), Options{
		Path: self(t), Env: childEnv("__sleep-ms 300"),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if res.WallMS < 250 || res.WallMS > 15000 {
		t.Fatalf("wall %v outside [250ms, 15s]", res.WallMS)
	}
}

func TestStdoutCapture(t *testing.T) {
	env := append(childEnv("__echo-out"), "HPOV_TEST_MSG=hello-hpov")
	res, err := Run(context.Background(), Options{
		Path: self(t), Env: env,
		CaptureStdout: true, Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// go test captures child stdout; the message must round-trip.
	if res.Stdout != "hello-hpov" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestTimeoutKills(t *testing.T) {
	start := time.Now()
	res, err := Run(context.Background(), Options{
		Path: self(t), Env: childEnv("__sleep-ms 30000"),
		Timeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("want TimedOut")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("timeout kill took %v", elapsed)
	}
}

func TestPeakTracked(t *testing.T) {
	res, err := Run(context.Background(), Options{
		Path: self(t), Env: childEnv("__sleep-ms 100"),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PeakRSSBytes <= 0 {
		t.Fatalf("peak %d: kernel-tracked peak must be positive", res.PeakRSSBytes)
	}
	if res.PeakSemantics == "" || res.PeakSemantics == "unavailable" {
		t.Fatalf("semantics = %q", res.PeakSemantics)
	}
	t.Logf("peak=%d semantics=%s", res.PeakRSSBytes, res.PeakSemantics)
}

func TestRealBinary(t *testing.T) {
	if os.Getenv("HPOV_E2E") == "" {
		t.Skip("real-binary spawn smoke needs HPOV_E2E=1")
	}
	p, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	res, err := Run(context.Background(), Options{
		Path: p, Args: []string{"version"}, CaptureStdout: true, Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.Stdout == "" {
		t.Fatalf("go version: exit=%d out=%q", res.ExitCode, res.Stdout)
	}
}
