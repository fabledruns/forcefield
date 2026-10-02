//go:build windows

package collect

import (
	"os"
	"strings"
	"testing"
	"unsafe"
)

// processMemoryCountersSize pins the ABI layout the lazy psapi call
// depends on. A mismatch here would silently misread every field, so it
// is asserted rather than assumed.
func TestProcessMemoryCountersLayout(t *testing.T) {
	var mc processMemoryCounters
	if got := unsafe.Sizeof(mc); got != 80 {
		t.Fatalf("sizeof(PROCESS_MEMORY_COUNTERS) = %d, want 80 on 64-bit", got)
	}
	off := unsafe.Offsetof(mc.WorkingSetSize)
	if off != 16 {
		t.Fatalf("WorkingSetSize offset = %d, want 16", off)
	}
	// PeakWorkingSetSize must precede WorkingSetSize in memory; reading
	// the wrong pair would swap current and peak.
	if unsafe.Offsetof(mc.PeakWorkingSetSize) >= off {
		t.Fatal("peak must precede working set in the structure")
	}
}

// TestPsapiReadsKnownChild is the check that matters on Windows: the
// psapi path must return the real resident size of a process with a
// known allocation, not zero and not a virtual-memory figure.
func TestPsapiReadsKnownChild(t *testing.T) {
	_, cleanup := startChild(t)
	defer cleanup()

	self, err := ReadingOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	child, err := ReadingOf(lastPID())
	if err != nil {
		t.Fatalf("child reading: %v", err)
	}
	if !child.Ok() {
		t.Fatalf("child unreadable: %+v", child)
	}
	if child.RSSSemantics != "windows:working_set" {
		t.Fatalf("semantics = %q", child.RSSSemantics)
	}
	if child.PeakSemantics != "windows:peak_working_set" {
		t.Fatalf("peak semantics = %q", child.PeakSemantics)
	}
	if child.RSSBytes < self.RSSBytes+longLivedChild/2 {
		t.Fatalf("child working set %d vs self %d: psapi is not reading real memory",
			child.RSSBytes, self.RSSBytes)
	}
	if child.PeakBytes < child.RSSBytes {
		t.Fatalf("peak %d below current %d", child.PeakBytes, child.RSSBytes)
	}
}

// TestPsapiDirectCall pins the raw wrapper's return values.
func TestPsapiDirectCall(t *testing.T) {
	ws, peak, err := ReadProcessMemoryInfo(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if ws == 0 {
		t.Fatal("working set must be non-zero for a live process")
	}
	if peak < ws {
		t.Fatalf("peak %d below current %d", peak, ws)
	}
	// A working set is resident memory, so it must be far below the
	// process image size: a virtual-memory mix-up would show up here.
	if ws > 1<<40 {
		t.Fatalf("working set %d looks like virtual memory", ws)
	}
}

func TestPsapiProcessGoneIsExited(t *testing.T) {
	pid := startAndReap(t)
	if _, _, err := ReadProcessMemoryInfo(pid); err == nil {
		t.Skip("pid reused; skip")
	}
	r, err := ReadingOf(pid)
	if err == nil {
		t.Skip("pid reused; skip")
	}
	if r.RSSBytes != -1 {
		t.Fatalf("exited process must report -1, got %d", r.RSSBytes)
	}
	if r.Reason == "" {
		t.Fatalf("exited process must carry a reason: %+v", r)
	}
}

func TestProcessPathOfKnownChild(t *testing.T) {
	path, err := ProcessPath(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if !strings.EqualFold(path, self) {
		t.Fatalf("path = %q, want %q", path, self)
	}
}

func TestToolhelpDescendantsFindsChild(t *testing.T) {
	cmd, cleanup := startChild(t, "HPOV_COLLECT_GRANDCHILD=1")
	defer cleanup()
	kids, err := descendants(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("descendants: %v", err)
	}
	if len(kids) == 0 {
		t.Skip("grandchild not visible yet")
	}
	for _, pid := range kids {
		if pid == cmd.Process.Pid {
			t.Fatal("root must not be its own descendant")
		}
	}
}
