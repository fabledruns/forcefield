//go:build darwin

package collect

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestDarwinReadingHasNoLivePeak(t *testing.T) {
	r, err := ReadingOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !r.Ok() {
		t.Fatalf("unreadable: %+v", r)
	}
	if r.RSSSemantics != "darwin:resident_size" {
		t.Fatalf("semantics = %q", r.RSSSemantics)
	}
	// macOS exposes no live per-process peak: it must be unavailable
	// rather than approximated from one read.
	if r.PeakBytes != -1 || r.PeakSemantics != "" {
		t.Fatalf("live peak must be unavailable: %+v", r)
	}
}

func TestDarwinDescendantsIncludesChild(t *testing.T) {
	cmd, cleanup := startChild(t)
	defer cleanup()
	kids, err := descendants(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("descendants: %v", err)
	}
	if len(kids) != 0 {
		t.Fatalf("child has %d descendants, want 0: %v", len(kids), kids)
	}
}

func TestDarwinWalkerExcludesRoot(t *testing.T) {
	parent := map[int]int{10: 20, 20: 10, 30: 10}
	out := walkTree(parent, 10)
	for _, pid := range out {
		if pid == 10 {
			t.Fatal("root must not be its own descendant")
		}
	}
	if len(out) != 1 || out[0] != 30 {
		t.Fatalf("descendants = %v, want [30]", out)
	}
}

func TestDarwinSemanticsNotVirtual(t *testing.T) {
	r, err := ReadingOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.RSSSemantics, "vmsize") || strings.Contains(r.RSSSemantics, "commit") {
		t.Fatalf("RSS must not be virtual memory: %q", r.RSSSemantics)
	}
	_ = runtime.GOOS
}
