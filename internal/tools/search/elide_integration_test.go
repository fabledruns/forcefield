package search

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

// elidingNearestSymbol reproduces the reported evaluation: a symbol
// whose match sits on a very long line (a generated or minified
// blob) plus a normal short-line reference. The search must report both
// matches without the long line flooding the context, and must never
// drop the match it is reporting. The flagship match's column is 15010:
// "var s = \"" is 9 bytes and 15000 characters of p follow, so the
// match starts at byte 15009.
func TestSearchCode_ElidesLongLineKeepingMatch(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	long := strings.Repeat("p", 15000) + "NearestSymbol" + strings.Repeat("q", 15000)
	writeSearchCodeFile(t, dir+"/big.go",
		"package p\nvar s = \""+long+"\"\n// NearestSymbol is documented.\nfunc NearestSymbol() {}\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "NearestSymbol", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	const worstBeforeBytes = 1 << 20 // the reported case was ~9,265 emitted bytes/line
	if got := len(res.Content); got > worstBeforeBytes {
		t.Fatalf("output is %d bytes, far over the reported case (%d)", got, worstBeforeBytes)
	}
	flagship := "big.go:2:15010: " + elideHead
	// Every reported match line must still carry its own text.
	lines := strings.Split(strings.TrimSpace(res.Content), "\n")
	var sawLong, sawPlain bool
	for _, ln := range lines {
		if len(ln) > maxElidedLenForTest {
			t.Fatalf("line of %d bytes is not bounded:\n%.120s", len(ln), ln)
		}
		if strings.Contains(ln, "…[line truncated]") {
			t.Fatalf("stale pre-fix marker leaked:\n%.160s", ln)
		}
		switch {
		case strings.HasPrefix(ln, flagship):
			sawLong = true
			// The match sits mid-line, so bytes were dropped on both
			// sides of the window and both sides must say so.
			if !strings.HasSuffix(ln, elideTail) {
				t.Fatalf("long line's dropped tail is unmarked (line looks shorter than it is):\n%.160s … %.160s", ln[:160], ln[len(ln)-80:])
			}
			if !strings.Contains(ln, "NearestSymbol") {
				t.Fatalf("long line lost its match:\n%.160s", ln)
			}
		case strings.HasPrefix(ln, "big.go:3:") || strings.HasPrefix(ln, "big.go:4:"):
			sawPlain = true
			if !strings.Contains(ln, "NearestSymbol") {
				t.Fatalf("short line lost its match: %s", ln)
			}
		}
	}
	if !sawLong || !sawPlain {
		t.Fatalf("expected both the long and short-line matches, and the long one at column 15010, got:\n%s", res.Content)
	}
}

// maxElidedLenForTest is the largest a reported line may be: the byte
// cap, plus both markers, plus rune-snapping slack (each cut point can
// shift by up to three UTF-8 continuation bytes), plus the
// path:line:column prefix the caller prints before the text.
const maxElidedLenForTest = maxLineBytes + len(elideHead) + len(elideTail) + 6 + 20

// A single symbol searched across several small files must be reported
// compactly: the whole result stays far below the byte budget, so the
// common case is not paying for the outlier case.
func TestSearchCode_ConciseSymbolLookup(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, dir+"/a.go", "package p\nfunc Target() {}\n")
	writeSearchCodeFile(t, dir+"/b.go", "package p\n// Target is called from a.go\nfunc Other() { Target() }\n")
	writeSearchCodeFile(t, dir+"/c.go", "package p\nfunc Unrelated() {}\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "Target", "path": dir})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if strings.Contains(res.Content, "c.go") {
		t.Fatalf("false positive:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go:2:") || !strings.Contains(res.Content, "b.go:") {
		t.Fatalf("missing matches:\n%s", res.Content)
	}
	for _, ln := range strings.Split(strings.TrimSpace(res.Content), "\n") {
		if !strings.Contains(ln, "…") {
			continue // elision only applies to outlier lines
		}
		t.Fatalf("short line must be reported verbatim, got %.120s", ln)
	}
}

// The built-in walker (rg absent) must elide the same way, since it is
// the fallback path for hosts without ripgrep.
func TestSearchFiles_ElidesLongLineKeepingMatch(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("z", 9000) + "Target" + strings.Repeat("z", 9000)
	writeSearchCodeFile(t, dir+"/big.txt", "plain\n"+long+"\n")
	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "Target", "path": dir})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "Target") {
		t.Fatalf("lost the match:\n%s", res.Content)
	}
	for _, ln := range strings.Split(strings.TrimSpace(res.Content), "\n") {
		if len(ln) > 64<<10 {
			t.Fatalf("line not bounded: %d bytes", len(ln))
		}
	}
}

// A match bigger than the whole per-line budget must not bypass it:
// its head stays first, the remainder is marked with its size, and the
// reported start column still points at the real match. (This test used
// to search for a 417-byte literal it never actually verified stayed
// whole inside the budget branch; a >2000-byte regex match is what
// genuinely enters the oversized path.)
func TestSearchCode_LongPatternSurvivesElision(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	needle := "Z" + strings.Repeat("w", 2700) // one 2701-byte match
	writeSearchCodeFile(t, dir+"/big.go", "package p\nvar s = \""+
		strings.Repeat("q", 500)+needle+strings.Repeat("V", 4000)+"\"\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"pattern": "Z[a-z]{2600,}", "path": dir, "regex": true,
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	// The match starts at byte 509 of line two (9-byte prefix plus 500
	// q's): column 510.
	if !strings.Contains(res.Content, "big.go:2:510: ") {
		t.Fatalf("match column 510 missing:\n%.300s", res.Content)
	}
	var long string
	for _, ln := range strings.Split(strings.TrimSpace(res.Content), "\n") {
		if strings.HasPrefix(ln, "big.go:2:510: ") {
			long = ln
		}
	}
	if long == "" {
		t.Fatalf("long match line missing:\n%s", res.Content)
	}
	if len(long) > maxElidedLenForTest {
		t.Fatalf("oversized match broke the per-line budget: %d bytes", len(long))
	}
	if !strings.Contains(long, "[match elided: 2701 bytes]") {
		t.Fatalf("oversized match not marked with its size:\n%.200s", long)
	}
	if !strings.HasPrefix(long, "big.go:2:510: "+elideHead) || !strings.HasSuffix(long, elideTail) {
		t.Fatalf("oversized match line not marked on both sides:\n%.120s … %.80s", long[:120], long[len(long)-80:])
	}
	if !strings.Contains(long, "Z"+strings.Repeat("w", 100)) {
		t.Fatalf("match head not kept:\n%.160s", long)
	}
	if strings.Contains(long, needle) {
		t.Fatalf("the 2701-byte match was emitted whole, not bounded:\n%.160s", long)
	}
}

// A regex match longer than the elision margin (400 bytes) must survive
// whole: rg's --json events carry the exact submatch span, and the
// elider preserves everything it reported finding. The reported column
// still locates the match.
func TestSearchCode_RegexLongMatchSurvivesElision(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	match := "Symbol" + strings.Repeat("X", 700) + "!" // 707 bytes, > elideMargin
	writeSearchCodeFile(t, dir+"/re.go", "package p\nvar s = \""+
		strings.Repeat("p", 9000)+match+strings.Repeat("q", 9000)+"\"\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"pattern": "Symbo[l]X{700}!", "path": dir, "regex": true,
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	// The reported column locates the match exactly...
	if !strings.Contains(res.Content, "re.go:2:9010:") {
		t.Fatalf("match column missing:\n%.300s", res.Content)
	}
	// ...and the whole 707-byte match is inside the emitted text: with
	// a match of 707 bytes, a window that kept only 400 bytes of it
	// would silently clip the rest while still claiming to report the
	// full match.
	if !strings.Contains(res.Content, match) {
		t.Fatalf("long regex match was clipped mid-match:\n%.300s", res.Content)
	}
	if strings.Contains(res.Content, "[match elided:") {
		t.Fatalf("a 707-byte match fits the 2000-byte budget; it must be emitted whole:\n%.200s", res.Content)
	}
	for _, ln := range strings.Split(strings.TrimSpace(res.Content), "\n") {
		if len(ln) > maxElidedLenForTest {
			t.Fatalf("line not bounded: %d bytes", len(ln))
		}
	}
}

// strings.ToLower is not length-preserving for every letter: U+212A
// (KELVIN SIGN) lowers to a one-byte k, shrinking the lowered copy. An
// offset found in the lowered copy applies to the lowered bytes, so the
// old window could land thousands of bytes away from the real match and
// drop it. The walker must locate in the original bytes.
func TestSearchFiles_CaseFoldDriftKeepsMatch(t *testing.T) {
	dir := t.TempDir()
	// 2100 bytes of Kelvins (each shrinking by 2 bytes when lowered),
	// then 1500 bytes of padding, then the match: the old fold-based
	// offset drifted ~2000 bytes high, far beyond the 400-byte margin.
	line := strings.Repeat("\u212A", 700) + strings.Repeat("p", 1500) + "MARKX" + strings.Repeat("q", 1500)
	writeSearchCodeFile(t, dir+"/fold.txt", "plain\n"+line+"\n")
	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"pattern": "markx", "path": dir, "case_insensitive": "true",
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "fold.txt:2:") {
		t.Fatalf("match line missing:\n%.300s", res.Content)
	}
	if !strings.Contains(res.Content, "MARKX") {
		t.Fatalf("case-insensitive match lost to fold drift:\n%.300s", res.Content)
	}
	for _, ln := range strings.Split(strings.TrimSpace(res.Content), "\n") {
		if len(ln) > maxElidedLenForTest {
			t.Fatalf("line not bounded: %d bytes", len(ln))
		}
	}
}

// Multiple long lines in one search must each be elided, so a file of
// generated rows cannot add up to an unbounded result.
func TestSearchCode_ManyLongLinesAllBounded(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	// Each row is far longer than maxLineBytes, but the whole file must
	// stay under maxFileBytes or the tool skips it as a large file.
	const rows = 12
	const rowBytes = 9000
	var b bytes.Buffer
	for i := 0; i < rows; i++ {
		b.WriteString("package p\nvar s")
		b.WriteString(strings.Repeat("n", 10+i*4))
		b.WriteString(" = \"")
		b.WriteString(strings.Repeat("w", rowBytes))
		b.WriteString("Target")
		b.WriteString(strings.Repeat("w", rowBytes))
		b.WriteString("\"\n")
	}
	if b.Len() >= maxFileBytes {
		t.Fatalf("fixture is %d bytes, over the %d searchable cap", b.Len(), maxFileBytes)
	}
	writeSearchCodeFile(t, dir+"/many.go", b.String())
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "Target", "path": dir})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	var bounded int
	for _, ln := range strings.Split(strings.TrimSpace(res.Content), "\n") {
		if strings.Contains(ln, "Target") {
			bounded++
			if len(ln) > maxElidedLenForTest {
				t.Fatalf("line of %d bytes over bound:\n%.120s", len(ln), ln)
			}
		}
	}
	if bounded != rows {
		t.Fatalf("expected %d reported matches, got %d:\n%s", rows, bounded, res.Content)
	}
}

// A regex match on a long line is the awkward combination: the rg path
// has the column but not the match length, because a regex match's
// length varies per line. The window must still contain the match.
func TestSearchCode_RegexMatchOnLongLineKeepsMatch(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, dir+"/re.go", "package p\nvar s = \""+
		strings.Repeat("p", 9000)+"SymbolA"+strings.Repeat("q", 9000)+"\"\n")
	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"pattern": "Symbol[A-Z]", "path": dir, "regex": true,
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	// The reported column locates the match exactly...
	if !strings.Contains(res.Content, "re.go:2:9010:") {
		t.Fatalf("match column missing:\n%s", res.Content)
	}
	line := strings.Split(strings.TrimSpace(res.Content), "\n")[0]
	if !strings.Contains(line, "SymbolA") {
		t.Fatalf("regex match dropped from its own line:\n%.200s", line)
	}
	if len(line) > maxElidedLenForTest {
		t.Fatalf("line not bounded: %d bytes", len(line))
	}
}

// The same awkward combination on the built-in walker fallback, which
// computes both column and length itself.
func TestSearchFiles_RegexMatchOnLongLineKeepsMatch(t *testing.T) {
	dir := t.TempDir()
	writeSearchCodeFile(t, dir+"/re.txt", strings.Repeat("p", 9000)+"SymbolB"+strings.Repeat("q", 9000)+"\n")
	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		// search_files' "regex" argument is a string flag, unlike
		// search_code's boolean one.
		"pattern": "Symbol[BX]", "path": dir, "regex": "true",
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "SymbolB") {
		t.Fatalf("regex match dropped:\n%.200s", res.Content)
	}
}

// A KELVIN run in the middle of the match: every Kelvin shrinks 3
// bytes to 1 when lowered, so the folded needle covers 400 needle
// bytes but 3×400 = 1200 original bytes. The elision window is spent
// around the match's ORIGINAL span, so all 500 Kelvins stay visible.
// (The pre-fix walker passed len(needle) as the span, spending 400
// bytes around the match and dropping 2×400 Kelvin bytes.)
func TestSearchFiles_InsideMatchShrinkingRunesKept(t *testing.T) {
	dir := t.TempDir()
	// x*1000 (1000B) + 500 Kelvins (1500B) + y*1000 = 3500 bytes.
	line := strings.Repeat("x", 1000) + strings.Repeat("\u212A", 500) + strings.Repeat("y", 1000)
	writeSearchCodeFile(t, dir+"/fold2.txt", strings.Repeat("y", 1000)+"\n"+line+"\n")
	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"pattern": strings.Repeat("k", 400), "path": dir, "case_insensitive": "true",
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "fold2.txt:2:") {
		t.Fatalf("match line missing:\n%.300s", res.Content)
	}
	var kelvins int
	for _, ln := range strings.Split(strings.TrimSpace(res.Content), "\n") {
		kelvins += strings.Count(ln, "\u212A")
		if len(ln) > maxElidedLenForTest {
			t.Fatalf("line of %d bytes is not bounded:\n%.120s", len(ln), ln)
		}
	}
	// The whole 1500-byte Kelvin run sits inside the 2000-byte window
	// spent around the match: nothing of it may be lost.
	if kelvins < 500 {
		t.Fatalf("only %d Kelvins visible, want all 500 from the run", kelvins)
	}
}

// A match that is nothing but shrinking runes: 700 Kelvins (2100
// bytes) fold to a 700-byte needle. The match's TRUE span is 2100
// bytes, so the oversized branch must report "[match elided: 2100
// bytes]" (the pre-fix span, len(needle), reported 700) and keep the
// head bytes; the retained head is 651 whole Kelvins (keep =
// 2000-3-16-26 = 1955 bytes, snapped from 1955 back to the rune
// boundary 1953 = 3×651).
func TestSearchFiles_KelvinWholeLineSpanOversized(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("\u212A", 700) // 2100 bytes
	writeSearchCodeFile(t, dir+"/foldK.txt", line+"\n")
	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"pattern": strings.Repeat("k", 700), "path": dir, "case_insensitive": "true",
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "foldK.txt:1:") {
		t.Fatalf("match line missing:\n%.300s", res.Content)
	}
	for _, ln := range strings.Split(strings.TrimSpace(res.Content), "\n") {
		if !strings.HasPrefix(ln, "foldK.txt:1:") {
			continue
		}
		if len(ln) > maxElidedLenForTest {
			t.Fatalf("line of %d bytes is not bounded", len(ln))
		}
		if !strings.Contains(ln, "[match elided: 2100 bytes]") {
			t.Fatalf("oversized marker must carry the match's true span (2100), got:\n%.120s", ln)
		}
		if got := strings.Count(ln, "\u212A"); got != 651 {
			t.Fatalf("visible Kelvins = %d, want exactly 651 (keep 1955 snapped back to the 1953 boundary)", got)
		}
	}
}
