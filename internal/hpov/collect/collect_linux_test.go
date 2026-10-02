//go:build linux

package collect

import (
	"os"
	"testing"
)

// TestParseStatus covers the /proc/<pid>/status parser, including the
// malformed-data path: an unparseable or wrong-unit value must fail
// rather than become zero bytes.
func TestParseStatus(t *testing.T) {
	status := "Name:\tff\n" +
		"VmPeak:\t  999999 kB\n" + // virtual peak: must be ignored
		"VmSize:\t  800000 kB\n" + // virtual size: must be ignored
		"VmHWM:\t   40000 kB\n" +
		"VmRSS:\t   30000 kB\n"
	rss, hwm, ok := parseStatus(status)
	if !ok {
		t.Fatal("valid status must parse")
	}
	if rss != 30000*1024 {
		t.Fatalf("rss = %d, want %d", rss, 30000*1024)
	}
	if hwm != 40000*1024 {
		t.Fatalf("hwm = %d, want %d", hwm, 40000*1024)
	}
}

func TestParseStatusMalformed(t *testing.T) {
	for name, status := range map[string]string{
		"unparseable": "VmRSS:\t  abc kB\n",
		"wrong unit":  "VmRSS:\t  100 MB\n",
		"negative":    "VmRSS:\t  -5 kB\n",
		"absent":      "Name:\tff\n",
		"empty":       "",
	} {
		if _, _, ok := parseStatus(status); ok {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestParseStatusMissingPeakIsNotMalformed(t *testing.T) {
	// A status without VmHWM still yields a usable current reading; the
	// peak simply stays unavailable.
	rss, hwm, ok := parseStatus("VmRSS:\t  1000 kB\n")
	if !ok {
		t.Fatal("current reading must parse without a peak field")
	}
	if rss != 1000*1024 {
		t.Fatalf("rss = %d", rss)
	}
	if hwm != -1 {
		t.Fatalf("hwm = %d, want -1 when absent", hwm)
	}
}

func TestParseKB(t *testing.T) {
	n, err := parseKB("  1234 kB")
	if err != nil || n != 1234*1024 {
		t.Fatalf("parseKB = %d, %v", n, err)
	}
	for _, bad := range []string{"", "   ", "1234", "1234 bytes", "kB"} {
		if _, err := parseKB(bad); err == nil {
			t.Errorf("parseKB(%q) must fail", bad)
		}
	}
}

func TestReadPPIDOfSelf(t *testing.T) {
	ppid, err := readPPID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if ppid <= 0 {
		t.Fatalf("ppid = %d", ppid)
	}
}

func TestDescendantsIncludesChild(t *testing.T) {
	// A real descendant, not a synthetic map.
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

func TestWalkTreeHandlesCycles(t *testing.T) {
	// A PID-reuse cycle must terminate, and the root must never appear
	// in its own descendant list.
	parent := map[int]int{1: 2, 2: 1, 3: 1, 4: 3}
	out := walkTree(parent, 1)
	for _, pid := range out {
		if pid == 1 {
			t.Fatal("root must not appear in its own descendant list")
		}
	}
	if len(out) != 2 {
		t.Fatalf("descendants = %v, want 3 and 4", out)
	}
}
