package permissions

import (
	"testing"
)

// TestDecisionZeroValueFailsClosed pins the core safety property of the
// Decision ordering: an uninitialized Decision must confirm, never
// silently allow. A zero Rules, a missed map lookup, or a forgotten
// field all surface as Ask.
func TestDecisionZeroValueFailsClosed(t *testing.T) {
	var d Decision
	if d == Allow {
		t.Fatal("zero Decision is Allow: uninitialized decisions must never allow")
	}
	if d != Ask {
		t.Fatalf("zero Decision = %v, want Ask", d)
	}
}

// TestZeroRulesCheckAsks proves a Manager built from a zero Rules
// (e.g. a store that returned nothing) denies-by-asking for every tool
// instead of falling through to Allow.
func TestZeroRulesCheckAsks(t *testing.T) {
	m, err := NewManager(&memStore{rules: Rules{}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	for _, tool := range []string{"shell", "read_file", "write_file", "unknown_tool"} {
		if got := m.Check(tool); got != Ask {
			t.Errorf("Check(%q) on zero Rules = %v, want Ask", tool, got)
		}
	}
}

// TestDecisionSpellingRoundTrip guards the persistence contract across
// the reordering: String/ParseDecision must be exact inverses, because
// config.yaml stores decisions by spelling, never by int value.
func TestDecisionSpellingRoundTrip(t *testing.T) {
	for _, d := range []Decision{Ask, Deny, Allow} {
		got, err := ParseDecision(d.String())
		if err != nil {
			t.Fatalf("ParseDecision(%q): %v", d.String(), err)
		}
		if got != d {
			t.Errorf("round-trip %v -> %q -> %v, want identity", d, d.String(), got)
		}
	}
}
