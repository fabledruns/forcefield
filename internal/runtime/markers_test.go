package runtime

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"forcefield/internal/config"
)

// ffMarkerPrefix is Forcefield's own marker line discriminator, as
// emitted by internal/perfmark. This test parses its own product's
// protocol directly: HPOV is a separate project now, and a product test
// must not depend on the benchmark harness.
const ffMarkerPrefix = "ff-perf "

// ffMarkerEvents returns the marker event names in emission order from
// captured stderr. Only whole lines that begin with the prefix count;
// console output that merely contains it is not a marker.
func ffMarkerEvents(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, ffMarkerPrefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, ffMarkerPrefix))
		rest, _, _ = strings.Cut(rest, " ")
		if rest == "" {
			continue
		}
		out = append(out, rest)
	}
	return out
}

// ffMarkerSeen reports whether event was emitted at least once.
func ffMarkerSeen(stderr, event string) bool {
	for _, e := range ffMarkerEvents(stderr) {
		if e == event {
			return true
		}
	}
	return false
}

// TestMain re-execs the test binary as a marker-emitting child when
// HPOV_RUNTIME_MARKERS_CHILD=1. The child builds a real Runtime with
// an isolated home and exits 0; markers go to stderr. Modes:
//
//	new            runtime.New() (loads config: emits config-loaded)
//	newfromconfig  config.Load + NewFromConfig (must NOT emit config-loaded)
func TestMain(m *testing.M) {
	if os.Getenv("HPOV_RUNTIME_MARKERS_CHILD") != "1" {
		os.Exit(m.Run())
	}
	os.Exit(markersChildMain())
}

func markersChildMain() int {
	home := os.Getenv("HPOV_CHILD_HOME")
	if home == "" {
		return 2
	}
	_ = os.Setenv("USERPROFILE", home)
	_ = os.Setenv("HOME", home)
	// Keep the child off any real repo: a bare temp dir is a
	// definitive fast-path miss (no git spawn).
	work, err := os.MkdirTemp("", "hpov-rt-child")
	if err != nil {
		return 2
	}
	defer func() { _ = os.RemoveAll(work) }()
	if err := os.Chdir(work); err != nil {
		return 2
	}
	switch os.Getenv("HPOV_CHILD_MODE") {
	case "new":
		rt, err := New()
		if err != nil {
			return 3
		}
		_ = rt.Close()
		return 0
	case "newfromconfig":
		cfg, err := config.Load()
		if err != nil {
			return 3
		}
		rt, err := NewFromConfig(cfg)
		if err != nil {
			return 3
		}
		_ = rt.Close()
		return 0
	}
	return 2
}

func runMarkersChild(t *testing.T, mode string) (string, int) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	env := []string{
		"HPOV_RUNTIME_MARKERS_CHILD=1",
		"HPOV_CHILD_MODE=" + mode,
		"HPOV_CHILD_HOME=" + home,
		"FF_PERF_MARKERS=1",
		"PATH=" + os.Getenv("PATH"),
	}
	if os.Getenv("SystemRoot") != "" {
		env = append(env,
			"SystemRoot="+os.Getenv("SystemRoot"),
			"TEMP="+os.Getenv("TEMP"),
			"TMP="+os.Getenv("TMP"),
		)
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	cmd := exec.Command(self)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = nil
	err = cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("child start: %v", err)
		}
	}
	return stderr.String(), code
}

func TestNewMarkerOrder(t *testing.T) {
	stderr, code := runMarkersChild(t, "new")
	if code != 0 {
		t.Fatalf("child exit = %d\nstderr:\n%s", code, stderr)
	}
	events := ffMarkerEvents(stderr)
	// Headless order: main-entry comes from main.main (absent here;
	// the child starts inside the test binary). Config is loaded in
	// New() before newRuntime starts, so config-loaded precedes
	// runtime-init-start on every path — including the TUI path,
	// where tui.Start loads config before the background builder.
	wantPrefix := []string{"config-loaded", "runtime-init-start", "stage-skills"}
	if len(events) < len(wantPrefix) {
		t.Fatalf("events = %v\nstderr:\n%s", events, stderr)
	}
	if !reflect.DeepEqual(events[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("marker prefix = %v, want %v\nstderr:\n%s", events[:3], wantPrefix, stderr)
	}
	if n := strings.Count(stderr, "ff-perf config-loaded"); n != 1 {
		t.Fatalf("config-loaded emitted %d times, want exactly once", n)
	}
	if n := strings.Count(stderr, "ff-perf runtime-init-start"); n != 1 {
		t.Fatalf("runtime-init-start emitted %d times, want exactly once", n)
	}
}

func TestNewFromConfigSkipsConfigLoaded(t *testing.T) {
	// The TUI path loads config in tui.Start (its own config-loaded)
	// and builds via NewFromConfig: no second emission.
	stderr, code := runMarkersChild(t, "newfromconfig")
	if code != 0 {
		t.Fatalf("child exit = %d\nstderr:\n%s", code, stderr)
	}
	events := ffMarkerEvents(stderr)
	if len(events) == 0 || events[0] != "runtime-init-start" {
		t.Fatalf("events = %v, want runtime-init-start first", events)
	}
	if ffMarkerSeen(stderr, "config-loaded") {
		t.Fatalf("NewFromConfig must not emit config-loaded (events = %v)", events)
	}
}
