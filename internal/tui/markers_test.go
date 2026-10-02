package tui

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/session"
)

// TestMain re-execs the test binary as a marker-emitting child when
// HPOV_TUI_MARKERS_CHILD=1. The child renders Views in a fresh
// process (fresh once-guards) with FF_PERF_MARKERS=1; markers go to
// stderr. Modes:
//
//	placeholder  two placeholder Views (never ready)
//	ready        two placeholder Views, then ready Views
func TestMain(m *testing.M) {
	if os.Getenv("HPOV_TUI_MARKERS_CHILD") == "1" {
		os.Exit(tuiMarkersChildMain())
	}
	os.Exit(m.Run())
}

func tuiMarkersChildMain() int {
	m := newStartingModel(&config.Config{}, session.New(), nil)
	_ = m.View()
	_ = m.View()
	if os.Getenv("HPOV_CHILD_MODE") == "ready" {
		m.ready = true
		m.width, m.height = 80, 24
		_ = m.View()
		_ = m.View()
		_ = m.View()
	}
	return 0
}

func runTuiMarkersChild(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HPOV_TUI_MARKERS_CHILD=1",
		"HPOV_CHILD_MODE=" + mode,
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
	if err := cmd.Run(); err != nil {
		t.Fatalf("child: %v\nstderr:\n%s", err, stderr.String())
	}
	return stderr.String()
}

func countMarker(stderr, name string) int {
	n := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ff-perf "+name) {
			// "first-frame" is a prefix of "first-useful-frame" on
			// the wire ("ff-perf first-frame" vs "ff-perf
			// first-useful-frame"): match the full event token.
			rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "ff-perf "))
			if ev := strings.Fields(rest); len(ev) > 0 && ev[0] == name {
				n++
			}
		}
	}
	return n
}

func TestFirstUsefulFrameNeedsReady(t *testing.T) {
	stderr := runTuiMarkersChild(t, "ready")
	if got := countMarker(stderr, "first-frame"); got != 1 {
		t.Fatalf("first-frame fired %d times, want exactly once:\n%s", got, stderr)
	}
	if got := countMarker(stderr, "first-useful-frame"); got != 1 {
		t.Fatalf("first-useful-frame fired %d times, want exactly once:\n%s", got, stderr)
	}
	if strings.Index(stderr, "ff-perf first-frame ") >
		strings.Index(stderr, "first-useful-frame") {
		t.Fatalf("wrong marker order:\n%s", stderr)
	}
}

func TestPlaceholderNeverUseful(t *testing.T) {
	stderr := runTuiMarkersChild(t, "placeholder")
	if got := countMarker(stderr, "first-frame"); got != 1 {
		t.Fatalf("first-frame fired %d times:\n%s", got, stderr)
	}
	if got := countMarker(stderr, "first-useful-frame"); got != 0 {
		t.Fatalf("first-useful-frame fired on placeholder Views:\n%s", stderr)
	}
}

func TestFirstFrameFormatUnchanged(t *testing.T) {
	stderr := runTuiMarkersChild(t, "placeholder")
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ff-perf first-frame") {
			if !strings.Contains(line, " alloc=") || !strings.Contains(line, " sys=") {
				t.Fatalf("first-frame lost alloc/sys fields: %q", line)
			}
			return
		}
	}
	t.Fatalf("first-frame missing:\n%s", stderr)
}

func TestPlaceholderTextUnchanged(t *testing.T) {
	// Same-process: no markers involved, guards the placeholder path
	// the first-frame marker sits on.
	m := model{}
	if got := m.View(); !strings.Contains(got, "Starting Forcefield") {
		t.Fatalf("placeholder frame = %q", got)
	}
}
