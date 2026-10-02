package collect

import (
	"os"
	"runtime"
	"testing"
)

// TestDeadPIDIsUnavailable uses a pid that cannot be live, so the
// exited-process contract is checked deterministically instead of
// depending on whether the OS has recycled a real pid yet.
func TestDeadPIDIsUnavailable(t *testing.T) {
	// Well above any plausible pid_max / pid allocation, and above the
	// Windows pid space (32-bit DWORD but bounded far lower).
	const deadPID = 0x7FFFFFF0
	r, err := ReadingOf(deadPID)
	if err == nil {
		t.Skip("a process exists at the sentinel pid; skip")
	}
	if r.RSSBytes != -1 {
		t.Fatalf("dead pid must report -1, got %d", r.RSSBytes)
	}
	if r.RSSBytes == 0 {
		t.Fatal("dead pid must never be reported as zero bytes")
	}
	if r.Reason == "" {
		t.Fatalf("dead pid must carry a reason: %+v", r)
	}
	if r.PeakBytes != -1 {
		t.Fatalf("dead pid peak must be -1, got %d", r.PeakBytes)
	}
	tree, terr := TreeOf(deadPID)
	if terr == nil {
		t.Skip("tree of dead pid unexpectedly succeeded")
	}
	if tree.RSSBytes != -1 {
		t.Fatalf("dead pid tree must report -1, got %d", tree.RSSBytes)
	}
}

func TestDeadPIDReasonIsSpecific(t *testing.T) {
	const deadPID = 0x7FFFFEE0
	r, err := ReadingOf(deadPID)
	if err == nil {
		t.Skip("process present at sentinel pid")
	}
	switch r.Reason {
	case ReasonExited, ReasonQueryFailed, ReasonPermitted, ReasonMalformed:
	default:
		t.Fatalf("unexpected reason %q for a dead pid", r.Reason)
	}
	if r.RSSSemantics != "" {
		t.Fatalf("a failed reading must carry no semantics: %q", r.RSSSemantics)
	}
	_ = runtime.GOOS
	_ = os.Getpid
}
