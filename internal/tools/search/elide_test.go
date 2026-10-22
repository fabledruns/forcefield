package search

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// bounded reports the widest the elided line may be: the byte cap plus
// the two markers, with slack for rune-boundary snapping at both cut
// points (each can shift by up to three continuation bytes).
func maxElidedLen(t *testing.T, maxBytes int) int {
	return maxBytes + len(elideHead) + len(elideTail) + 6
}

func TestElideLine_ShortLinesUntouched(t *testing.T) {
	const cap = 200
	for _, text := range []string{
		"",
		"a",
		strings.Repeat("x", cap),
		strings.Repeat("y", cap+1),
	} {
		got := elideLine(text, 1, 1, cap)
		if text != strings.Repeat("y", cap+1) && got != text {
			t.Fatalf("text %d bytes altered: %q", len(text), got)
		}
		if text == strings.Repeat("y", cap+1) && got == text {
			t.Fatalf("text just over cap should be elided")
		}
	}
}

func TestElideLine_KeepsMatchAndBounds(t *testing.T) {
	const cap = 300
	const needle = "NearestSymbol_XYZ"
	cases := []struct {
		name    string
		text    string
		matchAt int // 1-based byte column of the match
	}{
		{"match_at_start", needle + strings.Repeat("a", 6000), 1},
		{"match_at_end", strings.Repeat("b", 6000) + needle, 6001},
		{"match_mid", strings.Repeat("c", 3000) + needle + strings.Repeat("d", 3000), 3001},
		{"match_just_in", strings.Repeat("e", cap-1) + needle, cap},
		{"match_just_out", strings.Repeat("f", cap) + needle, cap + 1},
		{"match_far_out", strings.Repeat("g", 15000) + needle, 15001},
		{"unicode_prefix", strings.Repeat("é", 4000) + needle, 8001},
		{"match_col_past_end", strings.Repeat("h", 4000) + needle, 99999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if idx := strings.Index(tc.text, needle); idx+1 != tc.matchAt && tc.matchAt > 0 && tc.matchAt != 99999 {
				t.Fatalf("test setup: matchAt=%d but needle at byte %d", tc.matchAt, idx+1)
			}
			got := elideLine(tc.text, tc.matchAt, len(needle), cap)
			if !utf8.ValidString(got) {
				t.Fatalf("elided line is not valid UTF-8")
			}
			if !strings.Contains(got, needle) {
				t.Fatalf("elided line lost the match (%d bytes): %.200s", len(got), got)
			}
			if len(got) > maxElidedLen(t, cap) {
				t.Fatalf("elided line is %d bytes, over the bound of %d", len(got), maxElidedLen(t, cap))
			}
			if !strings.Contains(got, elideHead) && !strings.Contains(got, elideTail) {
				t.Fatalf("elision not marked; reader cannot tell text was dropped: %.100s", got)
			}
		})
	}
}

// A window that cuts both before and after the match must mark both
// sides. The mid-line window used to emit only a leading ellipsis and
// silently drop everything after the window, so the line looked like
// it ended at the cut.
func TestElideLine_MarksBothElidedSides(t *testing.T) {
	const cap = 300
	const needle = "NEEDLE"
	text := strings.Repeat("c", 3000) + needle + strings.Repeat("d", 3000)
	got := elideLine(text, 3001, len(needle), cap)
	if !strings.HasPrefix(got, elideHead) {
		t.Fatalf("head marker missing: %.40s", got)
	}
	if !strings.HasSuffix(got, elideTail) {
		t.Fatalf("tail elision unmarked; line looks shorter than it is: …%.40s", got[len(got)-40:])
	}
	if !strings.Contains(got, needle) {
		t.Fatalf("lost the match: %.60s", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("elided line is not valid UTF-8")
	}
	if len(got) > maxElidedLen(t, cap) {
		t.Fatalf("elided line %d bytes over bound %d", len(got), maxElidedLen(t, cap))
	}
}

func TestElideLine_UnknownMatchLength(t *testing.T) {
	const cap = 300
	text := strings.Repeat("k", 5000) + "NEEDLE" + strings.Repeat("l", 5000)
	got := elideLine(text, 5001, 0, cap)
	if !strings.Contains(got, "NEEDLE") {
		t.Fatalf("lost match with len 0: %.200s", got)
	}
	if len(got) > maxElidedLen(t, cap) {
		t.Fatalf("over bound: %d > %d", len(got), maxElidedLen(t, cap))
	}
	if !utf8.ValidString(got) {
		t.Fatal("invalid UTF-8")
	}
}

func TestElideLine_SingleRuneOverCapJustOver(t *testing.T) {
	// Multi-byte runes straddling the cut point must not be split.
	const cap = 10
	text := strings.Repeat("é", 10) + "NEEDLE"
	got := elideLine(text, len(text)-len("NEEDLE")+1, len("NEEDLE"), cap)
	if !utf8.ValidString(got) {
		t.Fatal("cut inside a multi-byte rune")
	}
	if !strings.Contains(got, "NEEDLE") {
		t.Fatalf("lost match: %q", got)
	}
}

// A match bigger than the entire per-line budget is bounded: its head
// stays first, the remainder is marked with its size, and the whole
// match is never emitted.
func TestElideLine_OversizedMatchBoundedAndMarked(t *testing.T) {
	const cap = 300
	match := strings.Repeat("N", 5000)
	text := match + strings.Repeat("z", 10)
	got := elideLine(text, 1, len(match), cap)
	if len(got) > cap {
		t.Fatalf("oversized match emitted %d bytes over the %d budget", len(got), cap)
	}
	if !strings.Contains(got, "[match elided: 5000 bytes]") {
		t.Fatalf("oversized elision not marked: %.120s", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("N", 64)) {
		t.Fatalf("match head not kept first: %.60s", got)
	}
	if strings.Contains(got, match) {
		t.Fatalf("whole oversized match leaked (%d bytes): %.120s", len(got), got)
	}
	if !strings.HasSuffix(got, elideTail) {
		t.Fatalf("line tail not marked: …%.40s", got[len(got)-40:])
	}
	if !utf8.ValidString(got) {
		t.Fatal("oversized path emitted invalid UTF-8")
	}
}

// Degenerate columns do not point at a match, so the window lands at
// the start of the line. What must hold is that it stays bounded and
// valid UTF-8 rather than panicking or emitting a huge line.
func TestElideLine_DegenerateColumn(t *testing.T) {
	const cap = 300
	for _, col := range []int{0, -5, 1 << 30} {
		text := strings.Repeat("i", 4000) + "NEEDLE"
		got := elideLine(text, col, len("NEEDLE"), cap)
		if len(got) > maxElidedLen(t, cap) {
			t.Fatalf("col %d: %d bytes over bound", col, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("col %d: invalid UTF-8", col)
		}
		// Which end is elided depends on where the (clamped) window
		// landed, but at least one marker must be present so the reader
		// can tell text was dropped.
		if !strings.Contains(got, elideHead) && !strings.Contains(got, elideTail) {
			t.Fatalf("col %d: elision unmarked", col)
		}
	}
}

func TestElideLine_DegenerateArgs(t *testing.T) {
	// maxBytes <= 0 means "no cap"; must not panic or truncate.
	long := strings.Repeat("m", 5000) + "NEEDLE"
	if got := elideLine(long, 5001, 5, 0); got != long {
		t.Fatalf("maxBytes=0 must pass through")
	}
	if got := elideLine(long, 5001, 5, -1); got != long {
		t.Fatalf("maxBytes<0 must pass through")
	}
	// A column beyond the line must clamp, not panic, and still keep
	// the match it is reporting. (This assertion used to swallow its
	// own failure.)
	if got := elideLine(long, 1<<30, 5, 100); !strings.Contains(got, "NEEDLE") {
		t.Fatalf("clamped column lost the match: %q", got)
	}
	// A budget below the match's own length cannot promise the whole
	// match; one below even the marker text collapses to the marker,
	// which is still honest and bounded.
	if got := elideLine("NEEDLE", 1, 6, 4); got != "[match elided: 6 bytes]" {
		t.Fatalf("budget below the markers must report only the marker, got %q", got)
	}
}

// A line only a few bytes over the cap can keep itself whole: the
// window may cover the entire line when the match sits at the line
// end and a rune boundary (here the é straddling the byte cut)
// drags lo back to 0. Nothing was dropped, so no marker may be
// emitted. The old switch's default emitted head+keep for this
// shape and invented a drop of exactly one byte.
func TestElideLine_RetainedLineUnmarked(t *testing.T) {
	text := "\u00e9" + strings.Repeat("a", 1994) + "MATCH" // 2+1994+5 = 2001 bytes, 1 over cap
	got := elideLine(text, 1997, 5, maxLineBytes)          // col 1997: é(2)+1994 → match at byte 1996
	if got != text {
		t.Fatalf("whole-window line must come back untouched: got %d bytes (%q), want the untouched 2001", len(got), got)
	}
}

// snapStart and snapEnd always return a boundary within the line: a
// cut point at the line end (or beyond it) is already valid and must
// clamp, not index one past the last byte and panic. Previously an
// oversized slope could panic (index out of range) past text[n].
func TestSnapStartBoundaries(t *testing.T) {
	if got := snapStart("abcd", 4); got != 4 {
		t.Fatalf("snapStart at the line end = %d, want 4 (already a boundary)", got)
	}
	if got := snapStart("abcd", 99); got != 4 {
		t.Fatalf("snapStart past the line = %d, want clamp to 4", got)
	}
	// Mid-rune snap: an é starts at byte 1, so a cut inside it steps
	// back to its start.
	if got := snapStart("a\u00e9bc", 2); got != 1 {
		t.Fatalf("snapStart inside é = %d, want 1", got)
	}
	// snapEnd clamp pins (no panic today, keep it pinned).
	if got := snapEnd("abcd", 4); got != 4 {
		t.Fatalf("snapEnd at the line end = %d, want 4", got)
	}
	if got := snapEnd("abcd", 99); got != 4 {
		t.Fatalf("snapEnd past the line = %d, want clamp to 4", got)
	}
	if got := snapEnd("a\u00e9bc", 2); got != 3 {
		t.Fatalf("snapEnd inside é = %d, want 3", got)
	}
}

// An oversized match reported at a column equal to the line's last
// byte position: the whole match extends past the line, the kept head
// is empty, and the line keeps its head marker plus the size marker
// (col is clamped to off=len, treated as "match starts at the byte
// before the end"). Newly, clamping to the line end cannot panic.
func TestElideLine_OversizedColumnAtEndClamps(t *testing.T) {
	text := strings.Repeat("x", 4006)
	got := elideLine(text, 4007, 5000, 300)
	// off clamps to len(text)=4006; the kept slice is empty, so the
	// emission is exactly the head marker and the matched-size marker
	// (off>0, end==len(case: head only).
	want := elideHead + "[match elided: 5000 bytes]"
	if got != want {
		t.Fatalf("oversized column at line end = %q (%d bytes), want exact %q", got, len(got), want)
	}
	if len(got) > 320 {
		t.Fatalf("oversized column emission %d bytes, want bounded (~29)", len(got))
	}
}

// Every window shape, pinned exactly: both sides cut, head-only,
// tail-only, bare (nothing cut), and the oversized branch's four
// emission shapes plus its degenerate budget. All except bare are
// byte-identical to the pre-fix emissions; the bare case is the fix's
// new honest shape.
func TestElideLine_WindowShapeMatrix(t *testing.T) {
	needle := "NEEDLE"

	// both sides: mid-line match on a 6006-byte line; lo=3000 (the
	// match's own start), hi=3300 (off+cap), keep 300B.
	text := strings.Repeat("c", 3000) + needle + strings.Repeat("d", 3000)
	want := elideHead + needle + strings.Repeat("d", 294) + elideTail
	if got := elideLine(text, 3001, len(needle), 300); got != want {
		t.Fatalf("both-sides = %dB, want exact %dB", len(got), len(want))
	}

	// tail only: match at the line start; hi lands at off+cap=300, lo=0.
	text = needle + strings.Repeat("e", 6000)
	want = needle + strings.Repeat("e", 294) + elideTail
	if got := elideLine(text, 1, len(needle), 300); got != want {
		t.Fatalf("tail-only = %dB, want exact %dB", len(got), len(want))
	}

	// head only: match at the line end; hi clamps to the line end,
	// lo=5706.
	text = strings.Repeat("b", 6000) + needle
	want = elideHead + strings.Repeat("b", 294) + needle
	if got := elideLine(text, 6001, len(needle), 300); got != want {
		t.Fatalf("head-only = %dB, want exact %dB", len(got), len(want))
	}

	// bare: window covers the whole line (see RetainedLineUnmarked).
	text = "\u00e9" + strings.Repeat("a", 1994) + "MATCH"
	if got := elideLine(text, 1997, 5, maxLineBytes); got != text {
		t.Fatalf("bare = %d bytes, want the untouched 2001", len(got))
	}

	// oversized, tail marker: match starts at the line start, ends
	// before the line end; keep = cap - 3(…) - 16(…) - 26(marker) =
	// 255 bytes of the match head.
	match := strings.Repeat("N", 5000)
	text = match + strings.Repeat("z", 10)
	want = strings.Repeat("N", 255) + "[match elided: 5000 bytes]" + elideTail
	if got := elideLine(text, 1, len(match), 300); got != want {
		t.Fatalf("oversized tail = %dB, want exact %dB", len(got), len(want))
	}

	// oversized, head marker: match starts inside the line and runs
	// to the line end; nothing was dropped at the end.
	text = strings.Repeat("p", 10) + match
	want = elideHead + strings.Repeat("N", 255) + "[match elided: 5000 bytes]"
	if got := elideLine(text, 11, len(match), 300); got != want {
		t.Fatalf("oversized head = %dB, want exact %dB", len(got), len(want))
	}

	// oversized, both markers: match inside with room on both sides.
	text = strings.Repeat("a", 15) + match + strings.Repeat("z", 15)
	want = elideHead + strings.Repeat("N", 255) + "[match elided: 5000 bytes]" + elideTail
	if got := elideLine(text, 16, len(match), 300); got != want {
		t.Fatalf("oversized both = %dB, want exact %dB", len(got), len(want))
	}

	// oversized, degenerate budget: the marker is the whole report
	// (pre-existing pin, kept in the matrix for one-line completeness).
	if got := elideLine("NEEDLE", 1, 6, 4); got != "[match elided: 6 bytes]" {
		t.Fatalf("oversized degenerate = %q, want the marker only", got)
	}
}
