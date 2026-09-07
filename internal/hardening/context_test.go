package hardening

import (
	"strings"
	"testing"

	"forcefield/internal/runtime"
)

// P1.15 lab: token/context budgeting. The question: can Forcefield guarantee
// requests stay within the provider window, including system + tools?
func TestTokenEstimationCJKBounded(t *testing.T) {
	// Reproduction: rune/4 undercounts CJK (~1 token/rune) ~4x, risking
	// window overshoot on CJK-heavy histories.
	cjk := strings.Repeat("漢", 4000) // 4000 runes
	est := runtime.EstimateTokens(cjk)
	// True cost is closer to 4000 tokens; estimate must not claim <=1500.
	if est < 2000 {
		t.Fatalf("CJK estimate dangerously low: %d for 4000 CJK runes (want >=2000)", est)
	}
}

func TestEstimateTokensAsciiSane(t *testing.T) {
	if got := runtime.EstimateTokens(""); got != 0 {
		t.Fatalf("empty = %d, want 0", got)
	}
	if got := runtime.EstimateTokens("hello world, this is a test"); got <= 0 {
		t.Fatalf("ascii estimate non-positive: %d", got)
	}
}
