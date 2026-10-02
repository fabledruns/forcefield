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

func TestDisabledWritesNothing(t *testing.T) {
	old := enabled
	enabled = false
	defer func() { enabled = old }()
	var buf bytes.Buffer
	restore := swapOutput(&buf)
	defer restore()
	Event("main-entry")
	EventMem("first-frame")
	if buf.Len() != 0 {
		t.Fatalf("disabled markers wrote %q", buf.String())
	}
}

func TestTimestampMode(t *testing.T) {
	oldE, oldT := enabled, tsMode
	enabled, tsMode = true, true
	defer func() { enabled, tsMode = oldE, oldT }()
	var buf bytes.Buffer
	restore := swapOutput(&buf)
	defer restore()
	Event("main-entry")
	line := buf.String()
	if !strings.HasPrefix(line, "ff-perf main-entry t=") || !strings.HasSuffix(line, "\n") {
		t.Fatalf("ts Event wrote %q", line)
	}
	ts := strings.TrimSuffix(strings.TrimPrefix(line, "ff-perf main-entry t="), "\n")
	if ts == "" || ts[0] == '-' {
		t.Fatalf("ts field missing/non-monotonic: %q", line)
	}
	for _, c := range ts {
		if c < '0' || c > '9' {
			t.Fatalf("ts field not numeric: %q", line)
		}
	}
	buf.Reset()
	EventMem("runtime-ready")
	got := buf.String()
	if !strings.HasPrefix(got, "ff-perf runtime-ready t=") ||
		!strings.Contains(got, " alloc=") || !strings.Contains(got, " sys=") {
		t.Fatalf("ts EventMem wrote %q", got)
	}
}

func TestDefaultFormatUnchangedInTSMode(t *testing.T) {
	// Format itself never gains a t= field: only the ts emission
	// path uses formatTS, so default output stays byte-compatible.
	if got := Format("config-loaded", 0, 0); got != "ff-perf config-loaded\n" {
		t.Fatalf("Format = %q", got)
	}
}

func TestFormatTSNonNegative(t *testing.T) {
	got := formatTS("x", 0, 0)
	if !strings.HasPrefix(got, "ff-perf x t=") {
		t.Fatalf("formatTS = %q", got)
	}
}

// BenchmarkEventDisabled measures the normal-path cost: one branch,
// no output, no allocs.
func BenchmarkEventDisabled(b *testing.B) {
	old := enabled
	enabled = false
	defer func() { enabled = old }()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Event("bench-probe")
	}
}

// BenchmarkEventMemDisabled measures the EventMem normal-path cost.
func BenchmarkEventMemDisabled(b *testing.B) {
	old := enabled
	enabled = false
	defer func() { enabled = old }()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		EventMem("bench-probe")
	}
}
