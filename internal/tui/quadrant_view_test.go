package tui

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// ---------------------------------------------------------------------------
// shared fixtures and assertion helpers
// ---------------------------------------------------------------------------

// quadTestQuadrants builds the four canvas regions with the plan's
// stable headers (§6.1) and short ASCII body lines tall enough to fill
// any tested region body. All content is single-cell ASCII so escape-free
// (Ascii profile) assertions can index cells directly by rune.
func quadTestQuadrants() [4]quadrantContent {
	lines := func(prefix string, n int) []string {
		s := make([]string, 0, n)
		for i := 0; i < n; i++ {
			s = append(s, fmt.Sprintf("%s-%02d", prefix, i))
		}
		return s
	}
	return [4]quadrantContent{
		{Label: "main agent", Lines: lines("Q0", 24)},
		{Label: "subagent-1", Lines: lines("Q1", 24)},
		{Label: "subagent-2", Lines: lines("Q2", 24)},
		{Label: "subagent-3", Lines: lines("Q3", 24)},
	}
}

// quadsEmpty is the fully blank region set (pure crosshair states).
func quadsEmpty() [4]quadrantContent {
	return [4]quadrantContent{}
}

// quadAnim returns a started animation advanced to dt past the test
// origin.
func quadAnim(dt time.Duration) *openingAnimState {
	a := newOpeningAnimForTest()
	a.Advance(animTestOrigin.Add(dt))
	return a
}

// quadRenderPlain renders a canvas under an escape-free Ascii profile
// and asserts the structural invariants (row count, exact cell widths,
// no escapes), returning the rows for rune-indexed assertions. Caller
// content must be single-cell ASCII only.
func quadRenderPlain(t *testing.T, cW, cH int, anim *openingAnimState, quads [4]quadrantContent) []string {
	t.Helper()
	rows := strings.Split(renderOrchestrationCanvas(cW, cH, anim, quads), "\n")
	if len(rows) != cH {
		t.Fatalf("canvas %dx%d: got %d rows, want %d", cW, cH, len(rows), cH)
	}
	for y, row := range rows {
		if strings.Contains(row, "\x1b") {
			t.Fatalf("canvas %dx%d row %d: Ascii profile leaked an escape: %q", cW, cH, y, row)
		}
		if cells := []rune(row); len(cells) != cW {
			t.Fatalf("canvas %dx%d row %d: width %d cells, want %d", cW, cH, y, len(cells), cW)
		}
	}
	return rows
}

// quadRenderStyled renders a canvas under any profile and asserts the
// structural invariants that hold with escapes present (row count and
// exact ANSI cell widths), returning the raw rows for styled-output
// assertions. Never use it to index cells by rune: escapes shift them.
func quadRenderStyled(t *testing.T, cW, cH int, anim *openingAnimState, quads [4]quadrantContent) []string {
	t.Helper()
	rows := strings.Split(renderOrchestrationCanvas(cW, cH, anim, quads), "\n")
	if len(rows) != cH {
		t.Fatalf("canvas %dx%d: got %d rows, want %d", cW, cH, len(rows), cH)
	}
	for y, row := range rows {
		if w := ansi.StringWidth(row); w != cW {
			t.Fatalf("canvas %dx%d row %d: ANSI width %d cells, want %d", cW, cH, y, w, cW)
		}
	}
	return rows
}

// quadCell fetches one cell of a rendered row with a precise message.
func quadCell(t *testing.T, rows []string, y, x int) rune {
	t.Helper()
	cells := []rune(rows[y])
	if x < 0 || x >= len(cells) {
		t.Fatalf("cell (%d,%d) out of range: row width %d", y, x, len(cells))
	}
	return cells[x]
}

// quadExpectText asserts the cells at column col of an escape-free row
// spell want exactly.
func quadExpectText(t *testing.T, row []rune, col int, want string) {
	t.Helper()
	w := []rune(want)
	if col+len(w) > len(row) {
		t.Fatalf("row width %d too short for %q at col %d", len(row), want, col)
	}
	for i, r := range w {
		if got := row[col+i]; got != r {
			t.Errorf("cell [%d]: got %q, want %q while checking %q", col+i, got, r, want)
			return
		}
	}
}

// quadExpectWindow asserts a contiguous slice of cells exactly matches
// want (band windows, art slices).
func quadExpectWindow(t *testing.T, cells []rune, col int, want []rune) {
	t.Helper()
	if col+len(want) > len(cells) {
		t.Fatalf("row width %d too short for window at col %d (width %d)", len(cells), col, len(want))
	}
	for i, r := range want {
		if got := cells[col+i]; got != r {
			t.Errorf("window cell [%d]: got %q, want %q", col+i, got, r)
			return
		}
	}
}

// quadExpectSpaces asserts cells from..to are all blank.
func quadExpectSpaces(t *testing.T, cells []rune, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		if cells[i] != ' ' {
			t.Errorf("cell [%d]: got %q, want a blank (from %d to %d)", i, cells[i], from, to)
			return
		}
	}
}

// quadExpectRule asserts cells from..to are all rule dashes.
func quadExpectRule(t *testing.T, cells []rune, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		if cells[i] != '─' {
			t.Errorf("cell [%d]: got %q, want '─'", i, cells[i])
			return
		}
	}
}

// dissolveRunes maps art runes through the §5.2.3 dissolve ladder.
func dissolveRunes(art []rune, alpha float64) []rune {
	out := make([]rune, 0, len(art))
	for _, r := range art {
		out = append(out, wordmarkFadeGlyph(r, alpha))
	}
	return out
}

// ruleSpan helpers: the horizontal rule at ph covers
// [xc−⌊ph·Xc⌋, xc) ∪ (xc, xc+⌊ph·wr⌋] inclusive-wise.
func leftRuleStart(ph float64, xc int) int {
	return xc - int(math.Floor(ph*float64(xc)))
}

func rightRuleEndInclusive(ph float64, xc, wr int) int {
	return xc + int(math.Floor(ph*float64(wr)))
}

// ---------------------------------------------------------------------------
// structural / mode / geometry
// ---------------------------------------------------------------------------

// TestQuadrantBannerArtParsable pins the compositor's art assumptions:
// three pre-split rows, exactly 58 rune-cells each (banner.go's "padded
// to asciiBannerWidth by construction" contract), and the 19-cell
// compact lockup with even letter spacing.
func TestQuadrantBannerArtParsable(t *testing.T) {
	if len(canvasBannerLines) != 3 {
		t.Fatalf("canvasBannerLines rows: got %d, want 3", len(canvasBannerLines))
	}
	for i, line := range canvasBannerRunes {
		if len(line) != asciiBannerWidth {
			t.Errorf("art row %d: %d runes, want %d", i, len(line), asciiBannerWidth)
		}
	}
	if len(compactLockupRunes) != compactLockupCells {
		t.Errorf("compact lockup: %d runes, want %d", len(compactLockupRunes), compactLockupCells)
	}
	if len(compactLockup) != compactLockupCells {
		// ASCII: 10 letters + 9 separating spaces = 19 cells.
		t.Errorf("compact lockup spacing broken: %q", compactLockup)
	}
}

// TestCanvasModeRouting pins the PLAN §6.2 threshold table verbatim.
func TestCanvasModeRouting(t *testing.T) {
	for _, tc := range []struct {
		cW, cH int
		want   canvasMode
	}{
		{0, 0, canvasStack},
		{47, 13, canvasStack},
		{47, 14, canvasStack},
		{48, 13, canvasStack},
		{48, 14, canvas2x2Compact},
		{63, 17, canvas2x2Compact},
		{63, 18, canvas2x2Compact},
		{64, 17, canvas2x2Compact},
		{64, 18, canvas2x2Full},
		{120, 40, canvas2x2Full},
		{121, 41, canvas2x2Full},
		{100, 25, canvas2x2Full},
		{30, 30, canvasStack},
		{80, 4, canvasStack},
	} {
		if got := canvasModeFor(tc.cW, tc.cH); got != tc.want {
			t.Errorf("canvasModeFor(%d,%d): got %v, want %v", tc.cW, tc.cH, got, tc.want)
		}
	}
}

// TestCanvasGeometryMath pins the §3.2 coordinate math for odd/even and
// degenerate sizes, including the non-negative guarantees.
func TestCanvasGeometryMath(t *testing.T) {
	for _, tc := range []struct {
		cW, cH int
		xc, yc int
		wl, wr int
		ht, hb int
	}{
		{121, 41, 60, 20, 60, 60, 20, 20},
		{120, 40, 60, 20, 60, 59, 20, 19},
		{100, 25, 50, 12, 50, 49, 12, 12},
		{64, 18, 32, 9, 32, 31, 9, 8},
		{2, 3, 1, 1, 1, 0, 1, 1},
		{1, 1, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 0, 0},
	} {
		g := canvasGeometryFor(tc.cW, tc.cH)
		if g.xc != tc.xc || g.yc != tc.yc || g.wl != tc.wl || g.wr != tc.wr || g.ht != tc.ht || g.hb != tc.hb {
			t.Errorf("canvasGeometryFor(%d,%d): got (xc=%d yc=%d wl=%d wr=%d ht=%d hb=%d), want (xc=%d yc=%d wl=%d wr=%d ht=%d hb=%d)",
				tc.cW, tc.cH, g.xc, g.yc, g.wl, g.wr, g.ht, g.hb,
				tc.xc, tc.yc, tc.wl, tc.wr, tc.ht, tc.hb)
		}
	}
}

// TestQuadrantMaskWidths pins the §3.2/§6.2 mask widths: 62 for the
// 58-column art (2 pads per side), 21 for the 19-cell compact lockup
// (1 pad per side).
func TestQuadrantMaskWidths(t *testing.T) {
	if got := maskWidthFull(wordmarkFull); got != 62 {
		t.Errorf("full mask width: got %d, want 62", got)
	}
	if got := maskWidthFull(wordmarkCompact); got != 21 {
		t.Errorf("compact mask width: got %d, want 21", got)
	}
}

// TestQuadrantGridGeometryAndOrdering renders the 2×2 grid at odd,
// even, and mixed sizes and pins: exact row/width invariants, band
// occlusion of the separator row, quadrant ordering (Q0 TL, Q1 TR,
// Q2 BL, Q3 BR), the 2-space label indent, and body placement.
func TestQuadrantGridGeometryAndOrdering(t *testing.T) {
	withOpeningProfile(t, termenv.Ascii)
	anim := quadAnim(2 * time.Second) // revealed, labels live, not fading
	quads := quadTestQuadrants()

	for _, size := range [][2]int{{121, 41}, {120, 40}, {100, 25}} {
		cW, cH := size[0], size[1]
		g := canvasGeometryFor(cW, cH)
		rows := quadRenderPlain(t, cW, cH, anim, quads)

		// Separator row at t=2s is a wordmark band row: the rule
		// survives only outside the ⌊α·62⌋ mask window.
		sep := []rune(rows[g.yc])
		quadExpectRule(t, sep, 0, g.xc-31)
		if got := sep[g.xc-31]; got != ' ' {
			t.Errorf("%dx%d sep row band left pad: got %q, want ' '", cW, cH, got)
		}
		if got := sep[g.xc]; got == '┼' || got == '─' {
			t.Errorf("%dx%d sep row center must be occluded by the wordmark mask, got %q", cW, cH, got)
		}
		if want := canvasBannerRunes[1][asciiBannerWidth/2]; sep[g.xc] != want {
			t.Errorf("%dx%d sep row center: got %q, want art cell bx=29 (%q)", cW, cH, sep[g.xc], want)
		}
		if got := sep[g.xc+30]; got != ' ' {
			t.Errorf("%dx%d sep row band right pad: got %q, want ' '", cW, cH, got)
		}
		quadExpectRule(t, sep, g.xc+31, cW)

		// Crosshair: content rows carry the vertical line at xc.
		for _, y := range []int{0, g.yc - 2, g.yc + 2, cH - 1} {
			if got := quadCell(t, rows, y, g.xc); got != '│' {
				t.Errorf("%dx%d cell (%d,%d): got %q, want '│'", cW, cH, y, g.xc, got)
			}
		}

		// Labels: 2-space indent at each region's first row. The
		// bottom-right header rides under the active band at t=2s, so
		// its row shows banner art row 2 instead of the label (the
		// label's post-restore return is pinned in the band-life test).
		quadExpectText(t, []rune(rows[0]), 0, "  main agent")
		quadExpectText(t, []rune(rows[0]), g.xc+1, "  subagent-1")
		quadExpectText(t, []rune(rows[g.yc+1]), 0, "  subagent-2")
		quadExpectWindow(t, []rune(rows[g.yc+1]), g.xc-29, canvasBannerRunes[2])

		// Body ordering: first body line of each region.
		quadExpectText(t, []rune(rows[1]), 0, "Q0-00")
		quadExpectText(t, []rune(rows[1]), g.xc+1, "Q1-00")
		quadExpectText(t, []rune(rows[g.yc+2]), 0, "Q2-00")
		quadExpectText(t, []rune(rows[g.yc+2]), g.xc+1, "Q3-00")
	}
}

// TestQuadrantSteadyStateCleanCross pins the nil-anim and post-fade
// renders: fully expanded separators, ┼ at the exact intersection, all
// labels visible, no wordmark glyphs anywhere.
func TestQuadrantSteadyStateCleanCross(t *testing.T) {
	withOpeningProfile(t, termenv.Ascii)
	quads := quadTestQuadrants()

	for _, anim := range []*openingAnimState{nil, quadAnim(10600 * time.Millisecond), quadAnim(time.Minute)} {
		rows := quadRenderPlain(t, 121, 41, anim, quads)

		sep := []rune(rows[20])
		quadExpectRule(t, sep, 0, 60)
		quadExpectRule(t, sep, 61, 121)
		if sep[60] != '┼' {
			t.Errorf("steady-state intersection: got %q, want '┼'", sep[60])
		}
		for _, y := range []int{0, 9, 30, 40} {
			if got := quadCell(t, rows, y, 60); got != '│' {
				t.Errorf("steady-state cell (60,%d): got %q, want '│'", y, got)
			}
		}
		quadExpectText(t, []rune(rows[0]), 0, "  main agent")
		quadExpectText(t, []rune(rows[0]), 61, "  subagent-1")
		quadExpectText(t, []rune(rows[21]), 0, "  subagent-2")
		quadExpectText(t, []rune(rows[21]), 61, "  subagent-3")

		out := strings.Join(rows, "\n")
		for _, block := range "█▀▄▓░" {
			if strings.ContainsRune(out, block) {
				t.Errorf("steady-state canvas still carries dissolve/block glyph %q", block)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// crosshair animation timeline
// ---------------------------------------------------------------------------

// TestQuadrantCrosshairTimeline renders the §4 phases 1–2 exactly:
// blank origin, partial vertical span with settled interior, and a
// partially overlapped horizontal rule under a not-yet-revealed band.
func TestQuadrantCrosshairTimeline(t *testing.T) {
	withOpeningProfile(t, termenv.Ascii)
	quads := quadsEmpty()
	const cW, cH, xc, yc = 100, 25, 50, 12

	// t = 0: blank terminal — nothing anywhere.
	out := strings.Join(quadRenderPlain(t, cW, cH, quadAnim(0), quads), "\n")
	if strings.ContainsAny(out, "─│┼") {
		t.Error("t=0 canvas renders separator glyphs before the expansion starts")
	}

	// t = 34ms: vertical partially expanded (pv ≈ 0.264): span
	// [yc−3, yc+3] with the center vertical passing through and no
	// horizontal rule yet.
	a := quadAnim(34 * time.Millisecond)
	pv := a.VertProgress()
	top := yc - int(math.Floor(pv*float64(yc)))
	bot := yc + int(math.Floor(pv*float64(cH-1-yc)))
	rows := quadRenderPlain(t, cW, cH, a, quads)
	for _, y := range []int{top, (top + yc) / 2, bot} {
		if got := quadCell(t, rows, y, xc); got != '│' {
			t.Errorf("t=34ms cell (%d,%d): got %q, want '│'", y, xc, got)
		}
	}
	if got := quadCell(t, rows, yc, xc); got != '│' {
		t.Errorf("t=34ms center row: got %q, want the vertical through Yc", got)
	}
	if y := top - 1; quadCell(t, rows, y, xc) != ' ' {
		t.Errorf("t=34ms row %d holds a separator outside the expansion span", y)
	}
	if y := bot + 1; quadCell(t, rows, y, xc) != ' ' {
		t.Errorf("t=34ms row %d holds a separator outside the expansion span", y)
	}

	// t = 450ms: vertical complete, horizontal half (ph = 0.875): the
	// rule spans [xc−43, xc−1] and [xc+1, xc+42+xc] on a 100-col grid;
	// the tip-by-frame derivation keeps this exact.
	a = quadAnim(450 * time.Millisecond)
	ph := a.HorizProgress()
	leftN := int(math.Floor(ph * float64(xc)))
	rightN := int(math.Floor(ph * float64(cW-1-xc)))
	rows = quadRenderPlain(t, cW, cH, a, quads)
	sep := []rune(rows[yc])
	if got := sep[xc]; got != '┼' {
		t.Errorf("t=450ms intersection: got %q, want '┼'", got)
	}
	quadExpectRule(t, sep, xc-leftN, xc)
	quadExpectRule(t, sep, xc+1, xc+rightN+1)
	quadExpectSpaces(t, sep, 0, xc-leftN)
	quadExpectSpaces(t, sep, xc+rightN+1, cW)
	for _, y := range []int{0, cH - 1} {
		if got := quadCell(t, rows, y, xc); got != '│' {
			t.Errorf("t=450ms edge row %d: got %q, want settled '│'", y, got)
		}
	}
}

// TestQuadrantCrosshairTipsColored checks the accent tip phase under a
// color terminal: mid-expansion tips carry the brand accent, interior
// cells carry the settled rule color.
func TestQuadrantCrosshairTipsColored(t *testing.T) {
	withOpeningProfile(t, termenv.ANSI)
	quads := quadsEmpty()
	const cW, cH, xc, yc = 100, 25, 50, 12

	// t = 250ms: vertical still expanding (pv ≈ 0.977), horizontal just
	// starting: the outermost reached cells are accent tips, the ones
	// behind are settled rule.
	a := quadAnim(250 * time.Millisecond)
	pv := a.VertProgress()
	dist := int(math.Floor(pv * float64(yc)))
	tipUp, tipDown := yc-dist, yc+dist
	rows := quadRenderStyled(t, cW, cH, a, quads)
	if !strings.Contains(rows[tipUp], canvasCrossStyle.Render("│")) {
		t.Errorf("t=250ms tip row %d lost its accent styling", tipUp)
	}
	if !strings.Contains(rows[tipDown], canvasCrossStyle.Render("│")) {
		t.Errorf("t=250ms tip row %d lost its accent styling", tipDown)
	}
	if !strings.Contains(rows[2], canvasSepStyle.Render("│")) {
		t.Error("t=250ms interior row 2 lost its settled rule styling")
	}
	if strings.Contains(rows[2], canvasCrossStyle.Render("│")) {
		t.Error("t=250ms interior row 2 is not settled (accent bled to interior)")
	}

	// t = 450ms: vertical complete — no tips anywhere below/above and
	// the ┼ carries the accent at the intersection.
	rows = quadRenderStyled(t, cW, cH, quadAnim(450*time.Millisecond), quads)
	for _, y := range []int{0, yc - 1, yc + 1, cH - 1} {
		if strings.Contains(rows[y], canvasCrossStyle.Render("│")) {
			t.Errorf("t=450ms row %d still carries an accent tip after full expansion", y)
		}
	}
	if !strings.Contains(rows[yc], canvasCrossStyle.Render("┼")) {
		t.Errorf("t=450ms row %d: intersection lost its accent styling", yc)
	}
}

// ---------------------------------------------------------------------------
// wordmark band reveal / fade / restore
// ---------------------------------------------------------------------------

// TestQuadrantWordmarkBandAndFade walks the band's whole life at the
// full flavor on an odd 121×41 grid (Xc=60): complete mask with pad
// columns, partial center-out reveal, α-shrunk fade window with the
// underlying crosshair reclaiming from the edges, and the fully
// restored composition at the exact 10.60s latch.
func TestQuadrantWordmarkBandAndFade(t *testing.T) {
	withOpeningProfile(t, termenv.Ascii)
	quads := quadTestQuadrants()
	const cW, cH, xc, yc = 121, 41, 60, 20
	maskFull := maskWidthFull(wordmarkFull) // 62 per §3.2
	glyphOrigin := xc - asciiBannerWidth/2  // 31

	// t = 2s: reveal complete, α = 1. Window = [xc−31, xc+30]; the
	// mask's quiet pads flank the art; content only outside the window.
	rows := quadRenderPlain(t, cW, cH, quadAnim(2*time.Second), quads)
	space2 := strings.Repeat(" ", canvasMaskPad)
	for i, artRow := range canvasBannerRunes {
		want := []rune(space2 + string(artRow) + space2)
		quadExpectWindow(t, []rune(rows[yc-1+i]), xc-31, want)
	}
	// Left content and the bottom-left header still visible left of the
	// window; right body text hides under the mask and must not leak.
	quadExpectText(t, []rune(rows[yc-1]), 0, "Q0-18")
	quadExpectText(t, []rune(rows[yc+1]), 0, "  subagent-2")
	quadExpectSpaces(t, []rune(rows[yc-1]), xc+31, cW)
	// Outside the band rows everything is the plain crosshair.
	if got := quadCell(t, rows, yc+2, xc); got != '│' {
		t.Errorf("t=2s row below the band: got %q, want '│'", got)
	}

	// t = 600ms: reveal mid-flight. pw = easeOutQuart(0.142857) ≈
	// 0.4602 → R = ⌊pw·31⌋ = 14; the horizontal is at ph ≈ 0.996, so
	// its rule has not yet reached both edges.
	a := quadAnim(600 * time.Millisecond)
	pw := a.WordmarkRevealProgress()
	R := int(math.Floor(pw * float64(maskFull/2)))
	sweepL, sweepR := xc-R, xc+R-1
	rows = quadRenderPlain(t, cW, cH, a, quads)
	cells := []rune(rows[yc])
	quadExpectWindow(t, cells, sweepL, canvasBannerRunes[1][sweepL-glyphOrigin:sweepR-glyphOrigin+1])
	// Only the center span is occluded; the bushes of rule and blank
	// edge keep the composition aligned.
	ph := a.HorizProgress()
	leftRuleStart := leftRuleStart(ph, xc)
	rightRuleEnd := rightRuleEndInclusive(ph, xc, cW-1-xc)
	quadExpectSpaces(t, cells, 0, leftRuleStart)
	quadExpectRule(t, cells, leftRuleStart, sweepL)
	quadExpectRule(t, cells, sweepR+1, rightRuleEnd+1)
	quadExpectSpaces(t, cells, rightRuleEnd+1, cW)

	// t = 10300ms: α = 0.5 exactly. maskW = ⌊31⌋ = 31 → window
	// [xc−15, xc+15]; art dissolves per §5.2.3 and the rule reclaims
	// the window-edge columns.
	a = quadAnim(10300 * time.Millisecond)
	alpha := a.WordmarkFadeAlpha()
	if math.Abs(alpha-0.5) > 1e-12 {
		t.Fatalf("α at t=10300ms: %v, want 0.5 (Phase 1 contract)", alpha)
	}
	maskW := int(math.Floor(alpha * float64(maskFull)))
	maskLeft := xc - maskW/2
	maskRight := maskLeft + maskW - 1
	rows = quadRenderPlain(t, cW, cH, a, quads)
	for i, artRow := range canvasBannerRunes {
		want := dissolveRunes(artRow[maskLeft-glyphOrigin:maskRight-glyphOrigin+1], alpha)
		quadExpectWindow(t, []rune(rows[yc-1+i]), maskLeft, want)
	}
	sep := []rune(rows[yc])
	quadExpectRule(t, sep, 0, maskLeft)
	quadExpectRule(t, sep, maskRight+1, cW)

	// t = 10599ms: α ≈ 5e-6 → the shrunken mask floor is empty; the
	// frame is already fully restored (graceful pre-latch termination).
	rows = quadRenderPlain(t, cW, cH, quadAnim(10599*time.Millisecond), quads)
	sep = []rune(rows[yc])
	quadExpectRule(t, sep, 0, xc)
	quadExpectRule(t, sep, xc+1, cW)
	if sep[xc] != '┼' {
		t.Errorf("t=10599ms intersection: got %q, want '┼'", sep[xc])
	}

	// t = 10600ms: latched steady state — the header is back under
	// where the band sat and no block/dissolve glyphs remain.
	rows = quadRenderPlain(t, cW, cH, quadAnim(10600*time.Millisecond), quads)
	quadExpectText(t, []rune(rows[yc+1]), 0, "  subagent-2")
	out := strings.Join(rows, "\n")
	for _, block := range "█▀▄▓░" {
		if strings.ContainsRune(out, block) {
			t.Errorf("t=10600ms canvas carries stale wordmark glyph %q", block)
		}
	}
}

// ---------------------------------------------------------------------------
// wordmark styles per terminal profile
// ---------------------------------------------------------------------------

// TestQuadrantWordmarkStylesAcrossProfiles checks each terminal
// profile's wordmark rendering at the completed-reveal state plus the
// fade midpoint: escape presence, the exact ANSI256 ramp values, the
// §5.2.3 dissolve, and zero escapes under Ascii.
func TestQuadrantWordmarkStylesAcrossProfiles(t *testing.T) {
	quads := quadsEmpty()
	const cW, cH, xc, yc = 121, 41, 60, 20

	for _, tc := range []struct {
		name string
		p    termenv.Profile
	}{
		{"TrueColor", termenv.TrueColor},
		{"ANSI256", termenv.ANSI256},
		{"ANSI", termenv.ANSI},
		{"Ascii", termenv.Ascii},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withOpeningProfile(t, tc.p)

			// t = 2s: fully revealed band, α = 1. The 58-rune art run
			// sits at [xc−29, xc+28]; the mask pads flank it at
			// xc−31/30 and xc+29/30.
			rows := strings.Split(renderOrchestrationCanvas(cW, cH, quadAnim(2*time.Second), quads), "\n")
			band := []rune(stripANSI(rows[yc]))
			quadExpectWindow(t, band, xc-29, canvasBannerRunes[1])
			switch tc.name {
			case "TrueColor":
				if !strings.Contains(rows[yc], "\x1b[") {
					t.Error("TrueColor band rendered without escapes")
				}
				// The gradient wave paints many distinct foregrounds
				// along the row; a collapsed gradient would repeat one.
				if seqs := distinctSGR(colorsInRow(rows[yc])); len(seqs) < 2 {
					t.Errorf("TrueColor gradient collapsed to %d distinct sequence(s)", len(seqs))
				}
				// One anchored cell must carry the exact Phase-1 math
				// color: bx=29 (center glyph column), art row 1, Δt=2s.
				color := string(wordmarkTrueColor(wordmarkGradientPhase(29, 1, 2*time.Second), 1.0))
				if !strings.Contains(rows[yc], ansiRGBSequence(color)) {
					t.Errorf("TrueColor band missing computed color %s", color)
				}
			case "ANSI256":
				if !strings.Contains(rows[yc], "38;5;204") {
					t.Error("ANSI256 band at α=1 missing ramp color 204")
				}
				if !strings.Contains(rows[yc], "\x1b[") {
					t.Error("ANSI256 band rendered without escapes")
				}
			case "ANSI":
				if !strings.Contains(rows[yc], "\x1b[") {
					t.Error("ANSI band rendered without escapes")
				}
			case "Ascii":
				for _, row := range rows {
					if strings.Contains(row, "\x1b") {
						t.Fatal("Ascii profile leaked an escape into the canvas")
					}
				}
			}

			// t = 10300ms: α = 0.5. ANSI256 steps to 125; ANSI16/Ascii
			// apply the §5.2.3 glyph dissolve; the visible window is
			// the ⌊α·62⌋ mask, and its edges reclaim real rule cells.
			rows = strings.Split(renderOrchestrationCanvas(cW, cH, quadAnim(10300*time.Millisecond), quads), "\n")
			band = []rune(stripANSI(rows[yc]))
			want := canvasBannerRunes[1][14:45] // bx [14..44] under the ⌊31⌋ mask
			switch tc.name {
			case "ANSI256":
				if !strings.Contains(rows[yc], "38;5;125") {
					t.Error("ANSI256 band at α=0.5 missing ramp color 125")
				}
				quadExpectWindow(t, band, 45, want)
			case "ANSI", "Ascii":
				quadExpectWindow(t, band, 45, dissolveRunes(want, 0.5))
			case "TrueColor":
				quadExpectWindow(t, band, 45, want)
				color := string(wordmarkTrueColor(wordmarkGradientPhase(29, 1, 10300*time.Millisecond), 0.5))
				if !strings.Contains(rows[yc], ansiRGBSequence(color)) {
					t.Errorf("TrueColor fade missing computed color %s", color)
				}
			}
			if got := band[44]; got != '─' {
				t.Errorf("%s at t=10300ms: cell [44] not reclaimed rule (got %q)", tc.name, got)
			}
			if got := band[76]; got != '─' {
				t.Errorf("%s at t=10300ms: cell [76] not reclaimed rule (got %q)", tc.name, got)
			}
		})
	}
}

// colorsInRow collects the TrueColor foreground sequences of a row.
func colorsInRow(row string) []string {
	var out []string
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			j := i + 1
			if j < len(row) && row[j] == '[' {
				j++
				for j < len(row) && (row[j] < 0x40 || row[j] > 0x7e) {
					j++
				}
				if j < len(row) {
					j++
				}
				if seq := row[i:j]; strings.Contains(seq, "38;2;") {
					out = append(out, seq)
				}
				i = j
				continue
			}
			i++
			continue
		}
		i++
	}
	return out
}

// distinctSGR collapses styled rows to their unique sequences.
func distinctSGR(seqs []string) map[string]struct{} {
	set := make(map[string]struct{}, len(seqs))
	for _, s := range seqs {
		set[s] = struct{}{}
	}
	return set
}

// ansiRGBSequence formats a #RRGGBB color as the decimal 24-bit SGR
// parameter list termenv emits, so tests can assert a computed
// TrueColor without depending on the bold/color join format.
func ansiRGBSequence(color string) string {
	if len(color) != 7 || color[0] != '#' {
		return color
	}
	bs, err := hex.DecodeString(color[1:])
	if err != nil || len(bs) != 3 {
		return color
	}
	return fmt.Sprintf("38;2;%d;%d;%d", bs[0], bs[1], bs[2])
}

// ---------------------------------------------------------------------------
// compact lockup flavor
// ---------------------------------------------------------------------------

// TestQuadrantCompactLockup covers the 48≤W<64 flavor on 60×20: the
// 19-cell spaced lockup rides the separator row inside its 21-cell
// mask, no 3-row art is ever painted, and reveal/fade work from the
// same Phase-1 progress values.
func TestQuadrantCompactLockup(t *testing.T) {
	withOpeningProfile(t, termenv.Ascii)
	quads := quadsEmpty()
	const cW, cH, xc, yc = 60, 20, 30, 10

	// t = 2s: complete lockup band [xc−10, xc+10]; pads single cells.
	rows := quadRenderPlain(t, cW, cH, quadAnim(2*time.Second), quads)
	quadExpectWindow(t, []rune(rows[yc]), xc-10, []rune(" "+compactLockup+" "))
	quadExpectRule(t, []rune(rows[yc]), 0, xc-10)
	quadExpectRule(t, []rune(rows[yc]), xc+11, cW)
	out := strings.Join(rows, "\n")
	if strings.ContainsRune(out, '█') {
		t.Error("compact canvas painted 3-row art runes")
	}
	if got := quadCell(t, rows, yc-1, xc); got != '│' {
		t.Errorf("compact row above the band: got %q, want '│'", got)
	}

	// t = 600ms: reveal mid-flight, R = ⌊pw·10⌋ = 4 center-out sweep;
	// rule flanks derived from the actual ph.
	a := quadAnim(600 * time.Millisecond)
	pw := a.WordmarkRevealProgress()
	R := int(math.Floor(pw * float64(maskWidthFull(wordmarkCompact)/2)))
	rows = quadRenderPlain(t, cW, cH, a, quads)
	cells := []rune(rows[yc])
	quadExpectWindow(t, cells, xc-R, compactLockupRunes[(xc-R)-(xc-compactLockupCells/2):xc+R-(xc-compactLockupCells/2)+1])
	ph := a.HorizProgress()
	leftStart := leftRuleStart(ph, xc)
	rightEnd := rightRuleEndInclusive(ph, xc, cW-1-xc)
	quadExpectSpaces(t, cells, 0, leftStart)
	quadExpectRule(t, cells, leftStart, xc-R)
	quadExpectRule(t, cells, xc+R+1, rightEnd+1)
	quadExpectSpaces(t, cells, rightEnd+1, cW)

	// t = 10600ms: fully restored rule + ┼, no wordmark.
	rows = quadRenderPlain(t, cW, cH, quadAnim(10600*time.Millisecond), quads)
	sep := []rune(rows[yc])
	quadExpectRule(t, sep, 0, xc)
	quadExpectRule(t, sep, xc+1, cW)
	if sep[xc] != '┼' {
		t.Errorf("compact t=10600ms intersection: got %q, want '┼'", sep[xc])
	}
}

// ---------------------------------------------------------------------------
// stack fallback
// ---------------------------------------------------------------------------

// TestQuadrantStackFallback covers the narrow/short branch: four labeled
// blocks divided by three full-width rules, main block first, heights
// apportioned from the leftover rows, and absolutely no grid glyphs.
func TestQuadrantStackFallback(t *testing.T) {
	withOpeningProfile(t, termenv.Ascii)
	anim := quadAnim(2 * time.Second)
	quads := quadTestQuadrants()

	for _, tc := range []struct {
		name     string
		cW, cH   int
		dividers [3]int
		labels   [4]int
	}{
		{"44x20", 44, 20, [3]int{5, 10, 15}, [4]int{0, 6, 11, 16}},
		{"80x12", 80, 12, [3]int{3, 6, 9}, [4]int{0, 4, 7, 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := quadRenderPlain(t, tc.cW, tc.cH, anim, quads)
			labels := []string{"  main agent", "  subagent-1", "  subagent-2", "  subagent-3"}
			for i := 0; i < 4; i++ {
				quadExpectText(t, []rune(rows[tc.labels[i]]), 0, labels[i])
				quadExpectText(t, []rune(rows[tc.labels[i]+1]), 0, fmt.Sprintf("Q%d-00", i))
			}
			for _, d := range tc.dividers {
				quadExpectRule(t, []rune(rows[d]), 0, tc.cW)
			}
			out := strings.Join(rows, "\n")
			for _, g := range "│┼█▀▄" {
				if strings.ContainsRune(out, g) {
					t.Errorf("stack canvas carries grid glyph %q", g)
				}
			}
		})
	}
}

// TestQuadrantStackLabelStagger checks the stack honors the §4 phase-5
// gating at 950ms: main block header present, the other three blank on
// exactly the rows they will occupy once revealed.
func TestQuadrantStackLabelStagger(t *testing.T) {
	withOpeningProfile(t, termenv.Ascii)
	quads := quadTestQuadrants()

	rows := quadRenderPlain(t, 80, 12, quadAnim(950*time.Millisecond), quads)
	quadExpectText(t, []rune(rows[0]), 0, "  main agent")
	for _, labelRow := range []int{4, 7, 10} {
		quadExpectSpaces(t, []rune(rows[labelRow]), 0, 12)
	}
	quadExpectText(t, []rune(rows[5]), 0, "Q1-00")

	rows = quadRenderPlain(t, 80, 12, quadAnim(1110*time.Millisecond), quads)
	quadExpectText(t, []rune(rows[4]), 0, "  subagent-1")
	quadExpectText(t, []rune(rows[7]), 0, "  subagent-2")
	quadExpectText(t, []rune(rows[10]), 0, "  subagent-3")
}

// ---------------------------------------------------------------------------
// degenerate and resize-bound sizes
// ---------------------------------------------------------------------------

// TestQuadrantTinySizesNoPanic exercises the smallest claimed
// dimensions in every mode and several timeline states: no panic, no
// escape leak, and the exact row/width counts, with the degenerate
// outputs explicitly pinned.
func TestQuadrantTinySizesNoPanic(t *testing.T) {
	withOpeningProfile(t, termenv.Ascii)
	quads := quadTestQuadrants()
	states := map[string]*openingAnimState{
		"steady": nil,
		"t0":     quadAnim(0),
		"reveal": quadAnim(600 * time.Millisecond),
		"fade":   quadAnim(10300 * time.Millisecond),
	}

	for _, size := range [][2]int{
		{0, 0}, {1, 1}, {2, 2}, {3, 3}, {5, 5},
		{47, 13}, {1, 25}, {25, 1}, {48, 14}, {63, 17}, {64, 18},
	} {
		cW, cH := size[0], size[1]
		for state, anim := range states {
			ok := t.Run(fmt.Sprintf("%dx%d-%s", cW, cH, state), func(t *testing.T) {
				out := renderOrchestrationCanvas(cW, cH, anim, quads)
				if strings.Contains(out, "\x1b") {
					t.Error("tiny canvas leaked an escape under Ascii")
				}
				if cH == 0 {
					if out != "" {
						t.Fatalf("0-height canvas: got %q, want empty", out)
					}
					return
				}
				rows := strings.Split(out, "\n")
				if len(rows) != cH {
					t.Fatalf("got %d rows, want %d", len(rows), cH)
				}
				for y, row := range rows {
					if cells := []rune(row); len(cells) != cW {
						t.Fatalf("row %d: width %d cells, want %d", y, len(cells), cW)
					}
				}
			})
			if !ok {
				return // parent test failed hard; stop stacking sizes
			}
		}
	}

	// Degenerate outputs pinned explicitly.
	if got := renderOrchestrationCanvas(0, 0, nil, quads); got != "" {
		t.Errorf("0×0: got %q, want empty", got)
	}
	if out := renderOrchestrationCanvas(25, 1, quadAnim(2*time.Second), quads); out != strings.Repeat(" ", 25) {
		t.Errorf("25×1: got %q, want a single blank row", out)
	}

	// 48×14 is the smallest compact grid: the lockup band really rides
	// the separator row once revealed.
	rows := strings.Split(renderOrchestrationCanvas(48, 14, quadAnim(2*time.Second), quads), "\n")
	quadExpectWindow(t, []rune(stripANSI(rows[7])), 15, compactLockupRunes)
}

// TestQuadrantResizeAndNarrowFallbackBounds is the PLAN §11 sweep over
// the reference and fallback sizes: every render keeps the exact line
// count and cell widths at the live state and mid-fade, with zero panic
// or malformed output.
func TestQuadrantResizeAndNarrowFallbackBounds(t *testing.T) {
	withOpeningProfile(t, termenv.TrueColor)
	quads := quadTestQuadrants()
	for _, size := range [][2]int{
		{120, 40}, {80, 24}, {60, 20}, {44, 20}, {80, 12}, {121, 41}, {89, 30}, {70, 19},
	} {
		cW, cH := size[0], size[1]
		for _, dt := range []time.Duration{2 * time.Second, 10300 * time.Millisecond} {
			rows := strings.Split(renderOrchestrationCanvas(cW, cH, quadAnim(dt), quads), "\n")
			if len(rows) != cH {
				t.Errorf("%dx%d at %v: %d lines, want %d", cW, cH, dt, len(rows), cH)
			}
			for y, row := range rows {
				if w := ansi.StringWidth(row); w != cW {
					t.Errorf("%dx%d at %v row %d: ANSI width %d, want %d", cW, cH, dt, y, w, cW)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// determinism and state purity
// ---------------------------------------------------------------------------

// TestQuadrantDeterminismAndNoStateMutation renders twice and demands
// byte-identical output under both a plain profile and a styled one,
// and proves none of the Phase-1 animation state moved during any
// render.
func TestQuadrantDeterminismAndNoStateMutation(t *testing.T) {
	quads := quadTestQuadrants()
	states := map[string]*openingAnimState{
		"live":    quadAnim(2 * time.Second),
		"midfade": quadAnim(10300 * time.Millisecond),
	}
	for _, size := range [][2]int{{121, 41}, {60, 20}, {50, 10}} {
		for _, p := range []termenv.Profile{termenv.Ascii, termenv.TrueColor} {
			withOpeningProfile(t, p)
			for name, anim := range states {
				snapshot := animSnapshot(anim)
				out1 := renderOrchestrationCanvas(size[0], size[1], anim, quads)
				out2 := renderOrchestrationCanvas(size[0], size[1], anim, quads)
				if out1 != out2 {
					t.Errorf("%s %dx%d at %v: two renders differ", name, size[0], size[1], p)
				}
				if got := animSnapshot(anim); got != snapshot {
					t.Errorf("%s: rendering mutated animation state: %q → %q", name, snapshot, got)
				}
			}
		}
	}
}

// animSnapshot captures every observable Phase-1 value in one string.
func animSnapshot(a *openingAnimState) string {
	if a == nil {
		return "nil"
	}
	return fmt.Sprintf("%v|%v|%v|%v|%v|%v|%v|%v|%v|%v",
		a.elapsed, a.phase, a.wordmarkDismissed, a.Started(),
		a.VertProgress(), a.HorizProgress(), a.WordmarkRevealProgress(),
		a.WordmarkFadeAlpha(), a.LabelVisible(0), a.LabelVisible(3))
}

// TestQuadrantLabelRevealGating pins the 70ms stagger on the canvas:
// 950ms shows only main agent, 970ms unlocks subagent-1, and label
// visibility never disturbs the line geometry around it.
func TestQuadrantLabelRevealGating(t *testing.T) {
	withOpeningProfile(t, termenv.Ascii)
	quads := quadTestQuadrants()
	const cW, cH, xc = 121, 41, 60

	rows := quadRenderPlain(t, cW, cH, quadAnim(950*time.Millisecond), quads)
	quadExpectText(t, []rune(rows[0]), 0, "  main agent")
	quadExpectSpaces(t, []rune(rows[0]), xc+1, xc+14)

	rows = quadRenderPlain(t, cW, cH, quadAnim(970*time.Millisecond), quads)
	quadExpectText(t, []rune(rows[0]), 0, "  main agent")
	quadExpectText(t, []rune(rows[0]), xc+1, "  subagent-1")
	quadExpectSpaces(t, []rune(rows[0]), xc+14, xc+20)
	// Bodies flow regardless of the header stagger.
	quadExpectText(t, []rune(rows[1]), xc+1, "Q1-00")
	// Reveal flips only label cells; the vertical stays put.
	if got := quadCell(t, rows, 0, xc); got != '│' {
		t.Errorf("label row vertical: got %q, want '│'", got)
	}
}

// ---------------------------------------------------------------------------
// cell-safety helper units
// ---------------------------------------------------------------------------

// TestQuadrantCellSafetyUnits exercises the clipping primitives
// directly: long content, ANSI-styled content, wide runes at the cut
// boundary, combining marks, exact boundary fits, newline flattening,
// and the finalizer's width pinning.
func TestQuadrantCellSafetyUnits(t *testing.T) {
	t.Run("fitRegionLine padding and cutting", func(t *testing.T) {
		if got := fitRegionLine("hello", 10); got != "hello     " {
			t.Errorf("pad short line: got %q", got)
		}
		if got := fitRegionLine("exactly10!!", 10); got != "exactly10!" {
			t.Errorf("cut over line: got %q", got)
		}
		if got := fitRegionLine("", 5); got != "     " {
			t.Errorf("empty line: got %q", got)
		}
		if got := fitRegionLine("", 0); got != "" {
			t.Errorf("zero width: got %q", got)
		}
		// Newlines would corrupt row assembly; flattened defensively.
		if got := fitRegionLine("a\nb", 4); got != "a b " {
			t.Errorf("newline flatten: got %q", got)
		}
	})

	t.Run("fitRegionLine wide runes", func(t *testing.T) {
		// A wide rune straddling the cut leaves its residual column
		// blank: the piece is exactly width cells either way.
		if got := fitRegionLine("汉", 1); ansi.StringWidth(got) != 1 {
			t.Errorf("wide-rune residual: %q width %d, want 1", got, ansi.StringWidth(got))
		}
		if got := fitRegionLine("汉汉汉", 5); got != "汉汉 " {
			t.Errorf("wide-rune pad: got %q, want \"汉汉 \"", got)
		}
	})

	t.Run("fitRegionLine combining marks", func(t *testing.T) {
		// "a" + U+0301 is one grapheme of one cell in x/ansi, so a
		// width-2 region keeps both lines intact and width-4 pads.
		line := "a\u0301b\u0301"
		if got := fitRegionLine(line, 2); got != line {
			t.Errorf("combining marks preserved: got %q, want %q", got, line)
		}
		if got := fitRegionLine(line, 4); got != line+"  " {
			t.Errorf("combining marks padded: got %q", got)
		}
	})

	t.Run("fitRegionLine ANSI styled", func(t *testing.T) {
		withOpeningProfile(t, termenv.TrueColor)
		styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Render("0123456789ABCDEF")
		got := fitRegionLine(styled, 6)
		if w := ansi.StringWidth(got); w != 6 {
			t.Errorf("styled cut width: got %d cells, want 6 (%q)", w, got)
		}
		if text := stripANSI(got); text != "012345" {
			t.Errorf("styled cut text: got %q, want \"012345\"", text)
		}
	})

	t.Run("cutANSIWindow geometry and resets", func(t *testing.T) {
		if got := cutANSIWindow("abcdef", 2, 4); got != "cd" {
			t.Errorf("plain window: got %q, want \"cd\"", got)
		}
		if got := cutANSIWindow("abcdef", 4, 2); got != "" {
			t.Errorf("empty window: got %q", got)
		}
		if got := cutANSIWindow("abcdef", 8, 10); got != "" {
			t.Errorf("past-end window: got %q", got)
		}
		if got := cutANSIWindow("abcdef", 0, 6); got != "abcdef" {
			t.Errorf("full window fast path: got %q", got)
		}
		withOpeningProfile(t, termenv.TrueColor)
		styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000")).Render("0123456789")
		cut := cutANSIWindow(styled, 3, 6)
		if w := ansi.StringWidth(cut); w != 3 {
			t.Errorf("styled window width: got %d, want 3", w)
		}
		if text := stripANSI(cut); text != "345" {
			t.Errorf("styled window text: got %q, want \"345\"", text)
		}
		if !endsInANSIReset(cut) {
			t.Errorf("style run severed by the window lost its reset: %q", cut)
		}
	})

	t.Run("finalizeRow pins exact widths", func(t *testing.T) {
		if got := finalizeRow("abc", 3); got != "abc" {
			t.Errorf("exact: got %q", got)
		}
		if got := finalizeRow("abc", 5); got != "abc  " {
			t.Errorf("pad: got %q", got)
		}
		if got := finalizeRow("abcdef", 3); got != "abc" {
			t.Errorf("truncate: got %q", got)
		}
		if got := finalizeRow("", 0); got != "" {
			t.Errorf("zero: got %q", got)
		}
		withOpeningProfile(t, termenv.TrueColor)
		styled := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ED2663")).Render("abstractideal")
		got := finalizeRow(styled, 8)
		if w := ansi.StringWidth(got); w != 8 {
			t.Errorf("styled truncate width: got %d, want 8", w)
		}
		if !endsInANSIReset(got) {
			t.Errorf("styled truncate lost its reset: %q", got)
		}
	})
}
