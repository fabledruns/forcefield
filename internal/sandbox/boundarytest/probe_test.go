package boundarytest

import (
	"strings"
	"testing"
)

// TestProbeReportsHonestly is a logic test, not a boundary test: it
// asserts the probe reports facts without ever claiming isolation.
func TestProbeReportsHonestly(t *testing.T) {
	c := Probe()
	if c.OS == "" || c.Arch == "" || c.Kernel == "" {
		t.Errorf("Probe() left identity fields empty: %+v", c)
	}
	if c.BoundaryEnforced {
		t.Errorf("Probe() claims an enforced boundary: %+v (no backend exists yet)", c)
	}
	if strings.TrimSpace(c.BoundaryDetail) == "" {
		t.Error("Probe() has no BoundaryDetail explaining the unenforced state")
	}
	report := c.Report()
	for _, want := range []string{"os=", "arch=", "kernel=", "boundary_enforced=false", "ff_require_boundary=", "landlock=", "landlock_abi="} {
		if !strings.Contains(report, want) {
			t.Errorf("Report() missing %q:\n%s", want, report)
		}
	}
	t.Logf("capability probe:\n%s", report)
}

// The placeholder skip canary lived here until the isolated backend
// landed; genuine boundary enforcement is now demonstrated by
// TestLandlockBoundaryEnforced in package sandbox (gated by
// RequireBoundary), and the required-mode CI job branches on the
// probe's landlock status instead of expecting a canary failure.
