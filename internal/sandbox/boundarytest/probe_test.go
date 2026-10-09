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
	for _, want := range []string{"os=", "arch=", "kernel=", "boundary_enforced=false", "ff_require_boundary="} {
		if !strings.Contains(report, want) {
			t.Errorf("Report() missing %q:\n%s", want, report)
		}
	}
	t.Logf("capability probe:\n%s", report)
}

// TestProbeCanarySkipsWithMarker is the intentionally-skipped canary:
// on every platform without an OS-enforced shell boundary it skips with
// the diagnosable marker. When P0-D lands, this canary gains a sibling
// behind RequireBoundary(t, "landlock") that runs instead of skipping
// on probe-positive machines. CI asserts the skip message format from
// the -v log.
func TestProbeCanarySkipsWithMarker(t *testing.T) {
	c := Probe()
	if c.BoundaryEnforced {
		t.Fatal("probe claims an enforced boundary with no backend; refusing to pass")
	}
	RequireBoundary(t, "landlock", "no Linux isolation backend exists yet (P0-D deferred); probe reports unenforced")
}
