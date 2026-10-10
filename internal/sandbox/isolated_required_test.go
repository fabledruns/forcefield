package sandbox

import (
	"testing"

	"forcefield/internal/sandbox/boundarytest"
)

// TestLandlockRequiredModeGate demonstrates required-mode refusal on
// any platform without executing anything: it skips with the boundary
// marker by default and fails with the required-mode marker when
// FF_REQUIRE_BOUNDARY lists landlock. Real enforcement is proven by
// TestLandlockBoundaryEnforced on capable Linux; this gate exists so
// the fail-closed CI branch has a test that is present on every
// platform (a -run pattern matching no test exits 0, which would
// masquerade as a pass).
func TestLandlockRequiredModeGate(t *testing.T) {
	boundarytest.RequireBoundary(t, "landlock",
		"this run demonstrates required-mode refusal only; real enforcement is proven by TestLandlockBoundaryEnforced on capable Linux")
}
