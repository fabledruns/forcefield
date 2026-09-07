package hardening

import (
	"testing"
)

// Boundary lab helpers live here. Loop/provider injection helpers live in
// package runtime where the unexported seam is accessible.

// lab uses boundary-level helpers only; runtime-loop injection lives in
// package runtime (same-package seam) to preserve modularity.
func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !contains(haystack, needle) {
		t.Fatalf("expected %q to contain %q", haystack, needle)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && search(haystack, needle))
}

func search(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
