package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateModelChromeConstantsPinned pins the window arithmetic in
// bytes so a chrome edit cannot silently widen the composed result: the
// head budget is the remainder after reserving the tail, the three
// separators, and the bounded marker plus notice.
func TestTruncateModelChromeConstantsPinned(t *testing.T) {
	if got := modelHeadBudget(); got != 5220 {
		t.Fatalf("modelHeadBudget() = %d, want 5220 (6000 − 600 − 3·2 − 38 − 136)", got)
	}
	if modelToolResultTailBytes != 600 {
		t.Fatalf("modelToolResultTailBytes = %d, want 600", modelToolResultTailBytes)
	}
	if windowMarkerMax != 38 {
		t.Fatalf("windowMarkerMax = %d, want 38 (19 + 9 digits + 10)", windowMarkerMax)
	}
	if truncationNoticeMax != 136 {
		t.Fatalf("truncationNoticeMax = %d, want 136 (127 literal base + 9 digits)", truncationNoticeMax)
	}
	if maxRowCleanSkim != 63 {
		t.Fatalf("maxRowCleanSkim = %d, want 63", maxRowCleanSkim)
	}
	if sizeDigitsReserve != 9 {
		t.Fatalf("sizeDigitsReserve = %d, want 9", sizeDigitsReserve)
	}
}

// TestTruncateModelComposedStaysWithinBudget pins the composed
// invariant: head + center marker + tail + notice never exceeds
// MaxModelToolResultChars bytes, stays valid UTF-8, opens with the head
// window, and closes with the truncation notice naming both sizes.
func TestTruncateModelComposedStaysWithinBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"ascii-6001", strings.Repeat("a", 6001)},
		{"ascii-6600", strings.Repeat("a", 6600)},
		{"ascii-20000", strings.Repeat("a", 20000)},
		{"ascii-200000", strings.Repeat("a", 200000)},
		{"runes-6600", strings.Repeat("世", 2200)},
		{"runes-mixed", strings.Repeat("a世", 4001)},
		{"rows-8000", strings.Repeat("row\n", 2000)},
		{"runes-mid-region", strings.Repeat("a", 4000) + strings.Repeat("界", 700) + "a"},
	} {
		if len(tc.content) <= MaxModelToolResultChars {
			t.Fatalf("%s: setup, content is only %d bytes", tc.name, len(tc.content))
		}
		out := TruncateModelToolResult(tc.content)
		if len(out) > MaxModelToolResultChars {
			t.Fatalf("%s: composed len = %d bytes, cap %d", tc.name, len(out), MaxModelToolResultChars)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("%s: composed result is not valid UTF-8", tc.name)
		}
		// Head fidelity: the window head survives whatever rune
		// alignment shaved off the head budget; no guard shrink runs on
		// the pinned path.
		if head := tc.content[:cutRuneBoundary(tc.content, modelHeadBudget())]; !strings.HasPrefix(out, head) {
			t.Fatalf("%s: composed result lost its head window", tc.name)
		}
		if !strings.Contains(out, "[...center elided: ") {
			t.Fatalf("%s: center-elision marker missing:\n%.80s", tc.name, out)
		}
		if !strings.Contains(out, "...output truncated at 6000") || !strings.HasSuffix(out, "if you need the rest.]") {
			t.Fatalf("%s: closing notice missing or malformed:\n%.80s", tc.name, out)
		}
	}
}

// TestTruncateModelTailKeepsRuneBoundary pins that a multibyte rune
// straddling the tail-start arithmetic is snapped forward, never split,
// and the composed result stays valid UTF-8: the rune region sits
// mid-content at an offset where len(content)−600 lands on a
// continuation byte.
func TestTruncateModelTailKeepsRuneBoundary(t *testing.T) {
	content := strings.Repeat("a", 4000) + strings.Repeat("界", 700) + "a"
	if len(content) != 6101 || !utf8.RuneStart(content[5503]) || utf8.RuneStart(content[5501]) {
		t.Fatalf("setup: tail boundary no longer straddles a rune (len %d)", len(content))
	}
	out := TruncateModelToolResult(content)
	if !utf8.ValidString(out) {
		t.Fatal("composed result is not valid UTF-8")
	}
	if len(out) > MaxModelToolResultChars {
		t.Fatalf("composed len = %d bytes", len(out))
	}
}

// TestTruncateModelTailSkimPrefersRowBoundary pins the row-clean skim:
// when a newline arrives within the skim limit, the tail opens on the
// line after it instead of mid-row.
func TestTruncateModelTailSkimPrefersRowBoundary(t *testing.T) {
	content := strings.Repeat("row\n", 2000) // 8000 bytes of 4-byte rows
	out := TruncateModelToolResult(content)
	tailAfter := strings.Index(out, " bytes...]\n\n")
	if tailAfter < 0 {
		t.Fatalf("marker or separators missing:\n%.120s", out)
	}
	tail := out[tailAfter+len(" bytes...]\n\n"):]
	if !strings.HasPrefix(tail, "row\n") {
		t.Fatalf("tail does not open on a fresh row:\n%.40s", tail)
	}
}

// TestTruncateModelTinyBudgetGuard pins the belt-and-braces chain:
// truncateModelResult called directly with a forced tiny budget still
// returns non-empty output within that budget, in valid UTF-8 (tail
// shrink, head shrink, hard rune-snapped clamp — the absolute stop).
func TestTruncateModelTinyBudgetGuard(t *testing.T) {
	for _, tt := range []struct {
		name  string
		max   int
		head  int
		tail  int
		size  int
		rows  bool
		runes bool
	}{
		{"tiny-all-ascii", 400, 60, 40, 5000, false, false},
		{"tiny-with-rows", 300, 50, 30, 4000, true, false},
		{"chrome-over-budget", 180, 20, 10, 2000, false, false},
		{"clamp-final-stop", 150, 20, 10, 2000, false, false},
		{"tiny-solid-runes", 320, 80, 60, 6000, false, true},
	} {
		content := strings.Repeat("x", tt.size)
		if tt.rows {
			content = strings.Repeat("row\n", tt.size/4)
		} else if tt.runes {
			content = strings.Repeat("界", tt.size/3)
		}
		out := truncateModelResult(content, tt.max, tt.head, tt.tail)
		if len(out) > tt.max {
			t.Fatalf("%s: composed len = %d bytes, forced budget %d", tt.name, len(out), tt.max)
		}
		if out == "" {
			t.Fatalf("%s: guard collapsed to empty output", tt.name)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("%s: guard output is not valid UTF-8", tt.name)
		}
	}
}

// TestTruncateModelTinyBudgetGuardMultibyte pins the multibyte-first corner
// of the guard chain: when the head budget is smaller than the first rune
// (forced tiny budgets, negative guard headroom) the head must become EMPTY
// rather than tear that rune — no torn leading byte may ever enter the
// output. Includes the auditor shapes (max=181, head=1, tail=0) on
// é-first content and (max=150, head=20, tail=10) on 界-first content,
// whose negative-headroom head shrink reaches the empty-head path.
func TestTruncateModelTinyBudgetGuardMultibyte(t *testing.T) {
	for _, tc := range []struct {
		name  string
		max   int
		head  int
		tail  int
		unit  string
		count int
	}{
		{"auditor-two-byte-e-acute", 181, 1, 0, "é", 300},  // 600 bytes
		{"auditor-three-byte-jie", 150, 20, 10, "界", 2000}, // 6000 bytes
		{"emoji-first", 200, 3, 5, "🙂", 120},               // 480 bytes
		{"two-byte-knife-edge", 190, 1, 2, "é", 300},
		{"zero-head-budget", 160, 0, 0, "世", 4000}, // 12000 bytes
	} {
		content := strings.Repeat(tc.unit, tc.count)
		if len(content) <= tc.max {
			t.Fatalf("%s: setup, content is only %d bytes", tc.name, len(content))
		}
		out := truncateModelResult(content, tc.max, tc.head, tc.tail)
		if len(out) > tc.max {
			t.Fatalf("%s: composed len = %d bytes, forced budget %d", tc.name, len(out), tc.max)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("%s: composed output is not valid UTF-8; a torn first rune leaked", tc.name)
		}
		if out == "" {
			t.Fatalf("%s: guard collapsed to empty output", tc.name)
		}
		if c := strings.Count(out, "[...center elided: "); c != 1 {
			t.Fatalf("%s: center markers = %d, want exactly one", tc.name, c)
		}
		if c := strings.Count(out, "[...output truncated at "); c != 1 {
			t.Fatalf("%s: advice markers = %d, want exactly one", tc.name, c)
		}
	}
}
