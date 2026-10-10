//go:build !linux

package boundarytest

import (
	"strings"
	"testing"
)

// TestQueryLandlockUnsupported pins the honest non-Linux behavior: no
// probe, no ABI, status unsupported. Runs on the Windows and macOS CI
// legs.
func TestQueryLandlockUnsupported(t *testing.T) {
	abi, status, detail := queryLandlock()
	if abi != -1 || status != "unsupported" || strings.TrimSpace(detail) == "" {
		t.Errorf("queryLandlock() = (%d, %q, %q), want (-1, unsupported, non-empty)", abi, status, detail)
	}
}
