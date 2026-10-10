//go:build linux

package boundarytest

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestClassifyLandlockErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"enosys", unix.ENOSYS, "unsupported"},
		{"wrapped enosys", fmt.Errorf("landlock ABI query: %w", unix.ENOSYS), "unsupported"},
		{"eopnotsupp", unix.EOPNOTSUPP, "disabled"},
		{"wrapped eopnotsupp", fmt.Errorf("landlock ABI query: %w", unix.EOPNOTSUPP), "disabled"},
		{"einval", unix.EINVAL, "error"},
		{"eperm", unix.EPERM, "error"},
		{"generic", fmt.Errorf("boom"), "error"},
	}
	for _, tc := range cases {
		if got := classifyLandlockErr(tc.err); got != tc.want {
			t.Errorf("classifyLandlockErr(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestQueryLandlockSeams exercises the unavailable and error states
// through the query seam so they do not depend on the test kernel.
func TestQueryLandlockSeams(t *testing.T) {
	orig := landlockABIQuery
	t.Cleanup(func() { landlockABIQuery = orig })

	landlockABIQuery = func() (int, error) { return 5, nil }
	if abi, status, detail := queryLandlock(); abi != 5 || status != "supported" || detail != "" {
		t.Errorf("stubbed ABI 5: got (%d, %q, %q), want (5, supported, \"\")", abi, status, detail)
	}

	landlockABIQuery = func() (int, error) { return -1, unix.ENOSYS }
	if abi, status, _ := queryLandlock(); abi != -1 || status != "unsupported" {
		t.Errorf("stubbed ENOSYS: got (%d, %q), want (-1, unsupported)", abi, status)
	}

	landlockABIQuery = func() (int, error) { return 0, nil }
	if abi, status, detail := queryLandlock(); abi != -1 || status != "error" || detail == "" {
		t.Errorf("impossible ABI 0: got (%d, %q, %q), want (-1, error, non-empty)", abi, status, detail)
	}
}

// TestQueryLandlockLiveConsistency runs the real version query and
// asserts only coherence, never availability: on kernels with Landlock
// the ABI must be >= 1 with status supported; elsewhere the status
// must be one of the unavailable vocabulary with abi -1. This passes
// on any kernel without pretending support exists.
func TestQueryLandlockLiveConsistency(t *testing.T) {
	abi, status, detail := queryLandlock()
	t.Logf("live landlock query: abi=%d status=%q detail=%q", abi, status, detail)
	switch status {
	case "supported":
		if abi < 1 {
			t.Errorf("status supported with abi %d, want >= 1", abi)
		}
		if detail != "" {
			t.Errorf("supported query carries detail %q, want empty", detail)
		}
	case "unsupported", "disabled", "error":
		if abi != -1 {
			t.Errorf("status %q with abi %d, want -1", status, abi)
		}
	default:
		t.Errorf("status %q outside the stable vocabulary", status)
	}
}

// TestProbeReportLandlockLine pins the machine-readable CI contract:
// exactly one landlock status line from the fixed vocabulary and one
// integer ABI line, regardless of kernel.
func TestProbeReportLandlockLine(t *testing.T) {
	lines := strings.Split(Probe().Report(), "\n")
	var status, abiLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "landlock=") && !strings.HasPrefix(l, "landlock_") {
			status = strings.TrimPrefix(l, "landlock=")
		}
		if strings.HasPrefix(l, "landlock_abi=") {
			abiLine = strings.TrimPrefix(l, "landlock_abi=")
		}
	}
	switch status {
	case "supported", "unsupported", "disabled", "error":
	default:
		t.Errorf("landlock status %q outside the stable vocabulary", status)
	}
	var abi int
	if _, err := fmt.Sscanf(abiLine, "%d", &abi); err != nil {
		t.Errorf("landlock_abi %q is not an integer", abiLine)
	}
	if status == "supported" && abi < 1 {
		t.Errorf("supported status with abi %d, want >= 1", abi)
	}
	if status != "supported" && abi != -1 {
		t.Errorf("status %q with abi %d, want -1", status, abi)
	}
}
