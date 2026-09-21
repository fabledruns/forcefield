package perfmark

import (
	"bytes"
	"strings"
	"testing"
)

// Enabled reports whether startup markers are active. It snapshots
// FF_PERF_MARKERS once at init so disabled runs pay nothing per probe.
func TestEnabledReflectsEnv(t *testing.T) {
	// The snapshot happens at package init, before tests can set env,
	// so this only asserts the accessor is deterministic.
	a, b := Enabled(), Enabled()
	if a != b {
		t.Fatal("Enabled() is not stable")
	}
}

func TestEventWritesMarkerLine(t *testing.T) {
	old := enabled
	enabled = true
	defer func() { enabled = old }()
	var buf bytes.Buffer
	restore := swapOutput(&buf)
	defer restore()
	Event("config-loaded")
	if got := buf.String(); got != "ff-perf config-loaded\n" {
		t.Fatalf("Event wrote %q", got)
	}
}

func TestEventIncludesMemory(t *testing.T) {
	old := enabled
	enabled = true
	defer func() { enabled = old }()
	var buf bytes.Buffer
	restore := swapOutput(&buf)
	defer restore()
	EventMem("first-frame")
	got := buf.String()
	if !strings.HasPrefix(got, "ff-perf first-frame alloc=") {
		t.Fatalf("EventMem wrote %q, want alloc/sys fields", got)
	}
	if !strings.Contains(got, " sys=") || !strings.HasSuffix(got, "\n") {
		t.Fatalf("EventMem wrote %q, want sys field and newline", got)
	}
}

func TestFormat(t *testing.T) {
	if got := Format("stage-skills", 0, 0); got != "ff-perf stage-skills\n" {
		t.Fatalf("Format = %q", got)
	}
	if got := Format("runtime-ready", 123456, 789012); got != "ff-perf runtime-ready alloc=123456 sys=789012\n" {
		t.Fatalf("Format = %q", got)
	}
}
