package tui

import (
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// This file implements the four-quadrant orchestration canvas compositor
// (PLAN §3.1/§3.2/§6.2/§9.3): a 2×2 grid of independently clipped agent
// regions divided by the animated central crosshair, with the FORCEFIELD
// wordmark spliced over the intersection during the opening window.
//
// The compositor owns geometry, clipping, and masking only. Region
// content arrives pre-composed from callers (Phase 3 slot providers),
// and nothing here invents or styles agent activity. The animation clock
// is not owned here either: every value on screen is read from the
// Phase 1 openingAnimState getters, so rendering never advances or
// mutates the timeline and identical inputs always produce identical
// output.
//
// Rows never get sliced by byte index. Every window cut runs through
// the ANSI- and grapheme-aware helpers of charmbracelet/x/ansi so wide
// runes, combining marks, and styled content survive either side of a
// boundary (PLAN §12.1).

// quadrantContent is the pre-composed content of one canvas region.
// Lines are already styled (or plain) by the caller and are rendered
// top-down, clipped, and never rewrapped.
type quadrantContent struct {
	// Label is the region header text (e.g. "main agent"), shown only
	// once the animation reveals the slot's stagger step.
	Label string
	// Lines are the body lines below the header.
	Lines []string
}

// canvasMode is the layout branch chosen for a canvas size (PLAN §6.2).
type canvasMode uint8

const (
	// canvasStack is the single-column stacked fallback for narrow or
	// short terminals (W < 48 or H < 14).
	canvasStack canvasMode = iota
	// canvas2x2Compact keeps the 2×2 grid and swaps the 3-row wordmark
	// for the 1-row spaced lockup (48 ≤ W < 64, or 14 ≤ H < 18).
	canvas2x2Compact
	// canvas2x2Full is the full grid with the 58-column 3-row wordmark
	// (W ≥ 64 and H ≥ 18).
	canvas2x2Full
)

// canvasModeFor routes a canvas size to its layout branch. The thresholds
// mirror PLAN §6.2 exactly; every size below the grid range falls back to
// the stack, so the grid renderer only ever runs with dimensions it can
// lay out safely.
func canvasModeFor(cW, cH int) canvasMode {
	switch {
	case cW < 48 || cH < 14:
		return canvasStack
	case cW < 64 || cH < 18:
		return canvas2x2Compact
	default:
		return canvas2x2Full
	}
}

// compactLockup is the 1-row spaced uppercase wordmark fallback of
// PLAN §6.2: ten letters separated by single spaces, 19 cells wide.
const compactLockup = "F O R C E F I E L D"

// compactLockupCells is compactLockup's exact display width.
const compactLockupCells = 19

// canvasMaskPad is the quiet negative-space cells beyond the art on each
// side of the full 58-column wordmark inside its occluding mask
// (58 + 2·2 = 62, PLAN §3.2).
const canvasMaskPad = 2

// canvasBannerLines pre-splits the asciiBanner art into its content rows
// so the per-frame compositor never re-parses the literal.
var canvasBannerLines = nonEmptyArtLines(asciiBanner)

// canvasBannerRunes pre-converts each art row to runes so a band cell
// lookup is a plain O(1) index instead of a per-frame scan. The art is
// ASCII block characters plus spaces, so one rune == one cell.
var canvasBannerRunes = artRuneRows()

// compactLockupRunes pre-converts the compact lockup the same way.
var compactLockupRunes = []rune(compactLockup)

// artRuneRows converts the canvasBannerLines art rows to rune slices.
func artRuneRows() [][]rune {
	rows := make([][]rune, 0, len(canvasBannerLines))
	for _, line := range canvasBannerLines {
		rows = append(rows, []rune(line))
	}
	return rows
}

// canvasGeometry is the resolved 2D layout of one canvas draw
// (PLAN §3.2): the separator intersection and the four region
// dimensions. All values stay non-negative so callers can never index
// out of range.
type canvasGeometry struct {
	cW, cH int
	xc, yc int // intersection (⌊W/2⌋, ⌊H/2⌋)
	wl, wr int // left/right region widths
	ht, hb int // top/bottom region heights
}

// canvasGeometryFor resolves the geometry for a canvas size.
func canvasGeometryFor(cW, cH int) canvasGeometry {
	g := canvasGeometry{cW: cW, cH: cH}
	if cW > 0 {
		g.xc = cW / 2
		g.wl = g.xc
		g.wr = max(0, cW-g.xc-1)
	}
	if cH > 0 {
		g.yc = cH / 2
		g.ht = g.yc
		g.hb = max(0, cH-g.yc-1)
	}
	return g
}

// renderOrchestrationCanvas renders the orchestration interface for the
// given canvas dimensions. It returns exactly cH lines joined with \n,
// each exactly cW cells wide (0×0 yields ""). anim may be nil, which
// renders the steady-state canvas: full separators, labels shown, no
// wordmark; a started animation drives the §4 opening sequence from its
// getters.
func renderOrchestrationCanvas(cW, cH int, anim *openingAnimState, quads [4]quadrantContent) string {
	switch canvasModeFor(cW, cH) {
	case canvasStack:
		return renderStackedCanvas(cW, cH, anim, quads)
	case canvas2x2Compact:
		return renderQuadrantGrid(cW, cH, anim, quads, wordmarkCompact)
	default:
		return renderQuadrantGrid(cW, cH, anim, quads, wordmarkFull)
	}
}

// wordmarkMode selects which central wordmark flavor the grid draws.
type wordmarkMode uint8

const (
	// wordmarkCompact draws the 19-cell spaced lockup on the separator
	// row (PLAN §6.2 narrow fallback).
	wordmarkCompact wordmarkMode = iota
	// wordmarkFull draws the 58-column 3-row asciiBanner spliced across
	// the band rows yc−1..yc+1 (PLAN §3.2).
	wordmarkFull
)

// renderStackedCanvas is the narrow/short-terminal fallback (PLAN §6.2):
// the four regions stacked top-down with full-width dividers between
// them, main region first. Leftover rows go to the top block; the build
// is trimmed or padded to exactly cH rows so tiny canvases stay safe.
func renderStackedCanvas(cW, cH int, anim *openingAnimState, quads [4]quadrantContent) string {
	if cH <= 3 {
		// No room for a meaningful block/divider stack: a deliberate
		// blank canvas beats arbitrary mid-stack rules.
		return blankCanvas(cW, cH)
	}
	avail := cH - 3 // 3 divider rows between 4 blocks
	base, rem := avail/4, avail%4
	heights := [4]int{base + rem, base, base, base}

	rows := make([]string, 0, cH)
	for i := 0; i < 4; i++ {
		rows = append(rows, stackBlockRows(quads[i], heights[i], i, anim)...)
		if i < 3 {
			rows = append(rows, canvasSepStyle.Render(strings.Repeat("─", cW)))
		}
	}
	for len(rows) > cH {
		rows = rows[:len(rows)-1]
	}
	for len(rows) < cH {
		rows = append(rows, "")
	}
	for i, r := range rows {
		rows[i] = finalizeRow(r, cW)
	}
	return strings.Join(rows, "\n")
}

// blankCanvas returns cH rows of exactly cW blank cells joined with \n.
func blankCanvas(cW, cH int) string {
	if cW <= 0 || cH <= 0 {
		return ""
	}
	return strings.Repeat(strings.Repeat(" ", cW)+"\n", cH-1) + strings.Repeat(" ", cW)
}

// stackBlockRows renders one stacked block: the header line (respecting
// the label stagger) plus body lines, blank-padded to exactly height
// rows so divider positions stay stable. A zero-height block
// contributes nothing.
func stackBlockRows(q quadrantContent, height, slot int, anim *openingAnimState) []string {
	if height <= 0 {
		return nil
	}
	rows := make([]string, 0, height)
	if anim != nil && !anim.LabelVisible(slot) {
		rows = append(rows, "")
	} else {
		rows = append(rows, indentBlock(quadLabelStyle.Render(q.Label), "  "))
	}
	for i := 0; i < height-1; i++ {
		line := ""
		if i < len(q.Lines) {
			line = q.Lines[i]
		}
		rows = append(rows, line)
	}
	return rows[:min(len(rows), height)]
}

// renderQuadrantGrid renders the 2×2 composition for grid-space sizes.
// wm selects the wordmark flavor (full 3-row art vs compact 1-row
// lockup).
func renderQuadrantGrid(cW, cH int, anim *openingAnimState, quads [4]quadrantContent, wm wordmarkMode) string {
	g := canvasGeometryFor(cW, cH)
	pv, ph := g.separatorProgress(anim)
	band := g.bindBand(anim, wm)

	rows := make([]string, cH)
	for y := 0; y < cH; y++ {
		pieces := g.rowPieces(y, anim, quads, pv, ph)
		row := pieces.left + pieces.mid + pieces.right
		if band.active {
			row = g.assembleRow(row, band, y)
		}
		rows[y] = finalizeRow(row, cW)
	}
	return strings.Join(rows, "\n")
}

// separatorProgress reads the animated separator expansion factors from
// the Phase 1 state. A nil animation means the steady state (fully
// expanded); a started-but-immature one renders from its progress.
func (g canvasGeometry) separatorProgress(anim *openingAnimState) (pv, ph float64) {
	if anim == nil {
		return 1, 1
	}
	if !anim.Started() {
		return 0, 0
	}
	return anim.VertProgress(), anim.HorizProgress()
}

// canvasRowPieces are the three fixed zones of one grid row: the left
// region cells [0..xc), the single center cell at xc, and the right
// region cells [xc+1..W). Each piece is exact-width before assembly, so
// a row starts out summing to exactly cW cells.
type canvasRowPieces struct {
	left, mid, right string
}

// rowPieces builds one row's pieces: the horizontal separator row at yc
// (dashes plus the center intersection cell), or a region content row
// with the animated vertical separator cell at the center column.
func (g canvasGeometry) rowPieces(y int, anim *openingAnimState, quads [4]quadrantContent, pv, ph float64) canvasRowPieces {
	if y == g.yc {
		return g.sepRowPieces(pv, ph)
	}
	return g.contentRowPieces(y, anim, quads, pv)
}

// sepRowPieces builds the horizontal separator row at yc: blank canvas
// outside the animated dash span (⌊ph·Xc⌋ / ⌊ph·(W−1−Xc)⌋ per PLAN §4
// phase 2), colorSeparator dashes where reached, and the center
// intersection cell.
func (g canvasGeometry) sepRowPieces(pv, ph float64) canvasRowPieces {
	leftN := int(math.Floor(ph * float64(g.xc)))
	rightN := int(math.Floor(ph * float64(g.wr)))

	left := strings.Repeat(" ", g.xc-leftN)
	if leftN > 0 {
		left += canvasSepStyle.Render(strings.Repeat("─", leftN))
	}
	right := ""
	if rightN > 0 {
		right = canvasSepStyle.Render(strings.Repeat("─", rightN))
	}
	right += strings.Repeat(" ", g.wr-rightN)
	return canvasRowPieces{left: left, mid: g.sepRowCenter(pv, ph), right: right}
}

// sepRowCenter renders the center cell of the separator row: blank
// before anything moves; the vertical passing through Yc while only the
// vertical is expanding (accent while its span is still just the center
// point, the leading tip of PLAN §4 phase 1, then the settled rule
// color); the accent ┼ once the horizontal reaches the center (§4 phase
// 2). In steady state (pv=ph=1) the intersection stays an accent ┼.
func (g canvasGeometry) sepRowCenter(pv, ph float64) string {
	switch {
	case ph > 0:
		return canvasCrossStyle.Render("┼")
	case pv > 0:
		top := int(math.Floor(pv * float64(g.yc)))
		bot := int(math.Floor(pv * float64(g.hb)))
		if pv < 1 && (top == 0 || bot == 0) {
			return canvasCrossStyle.Render("│")
		}
		return canvasSepStyle.Render("│")
	default:
		return " "
	}
}

// contentRowPieces builds a region content row: left/right region lines
// plus the animated vertical separator cell at the center column. Rows
// outside the current expansion span hold a plain space so column
// alignment stays fixed while the line unfolds (§4 phase 1).
func (g canvasGeometry) contentRowPieces(y int, anim *openingAnimState, quads [4]quadrantContent, pv float64) canvasRowPieces {
	mid := " "
	if pv > 0 {
		top := g.yc - int(math.Floor(pv*float64(g.yc)))
		bot := g.yc + int(math.Floor(pv*float64(g.hb)))
		if y >= top && y <= bot {
			if pv < 1 && (y == top || y == bot) {
				// Leading tips sparkle in the accent while the
				// separator is still unfolding, settling to the
				// rule color.
				mid = canvasCrossStyle.Render("│")
			} else {
				mid = canvasSepStyle.Render("│")
			}
		}
	}
	idx := g.regionRowIdx(y)
	left, leftSlot, right, rightSlot := quads[0], 0, quads[1], 1
	if y > g.yc {
		left, leftSlot, right, rightSlot = quads[2], 2, quads[3], 3
	}
	return canvasRowPieces{
		left:  fitRegionLine(quadRegionLine(left, idx, anim, leftSlot), g.wl),
		mid:   mid,
		right: fitRegionLine(quadRegionLine(right, idx, anim, rightSlot), g.wr),
	}
}

// regionRowIdx maps a canvas row to its row index inside the region
// (top regions count y from 0; bottom regions skip the separator row).
func (g canvasGeometry) regionRowIdx(y int) int {
	if y < g.yc {
		return y
	}
	return y - g.yc - 1
}

// quadRegionLine returns region q's idx-th line: row 0 is the header
// (blank until the animation reveals the slot; PLAN §4 phase 5), the
// rest flow into the provided body lines ungated.
func quadRegionLine(q quadrantContent, idx int, anim *openingAnimState, slot int) string {
	if idx == 0 {
		if anim != nil && !anim.LabelVisible(slot) {
			return ""
		}
		return indentBlock(quadLabelStyle.Render(q.Label), "  ")
	}
	i := idx - 1
	if i < 0 || i >= len(q.Lines) {
		return ""
	}
	return q.Lines[i]
}

// fitRegionLine clips a content line to exactly width display cells:
// over-length lines are cell-safe cut (escape sequences survive at the
// boundary and a reset closes any run the cut bisected), short lines
// are blank-padded. Wide runes straddling the cut leave the residual
// column blank so the piece stays exactly width cells and row geometry
// never shifts.
func fitRegionLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	// The compositor renders single lines only; embedded newlines would
	// corrupt row assembly and are flattened defensively.
	line = strings.ReplaceAll(line, "\r", "")
	if strings.Contains(line, "\n") {
		line = strings.ReplaceAll(line, "\n", " ")
	}
	if ansi.StringWidth(line) >= width {
		cut := cutANSIWindow(line, 0, width)
		if pad := width - ansi.StringWidth(cut); pad > 0 {
			return cut + strings.Repeat(" ", pad)
		}
		return cut
	}
	return line + strings.Repeat(" ", width-ansi.StringWidth(line))
}

// wordmarkBand is the resolved central-wordmark window for one canvas
// draw (PLAN §3.2/§4/§5.2): the canvas columns [drawnLeft..drawnRight]
// currently drawn — the reveal sweep intersected with the fade-shrunk
// mask — plus the per-cell render parameters. The band is inactive when
// that window is empty (never begun, fully faded, or dismissed), which
// the caller checks once per row.
type wordmarkBand struct {
	active      bool
	drawnLeft   int // canvas columns spelled by the mask this frame
	drawnRight  int
	glyphW      int // glyph run width in cells (58 full / 19 compact)
	glyphOrigin int // canvas column holding banner cell bx=0
	rows        int // band height (3 art rows / 1 lockup row)
	alpha       float64
	dt          time.Duration // elapsed, driving the gradient wave phase
}

// maskWidthFull is the occluding mask width at full opacity for each
// wordmark flavor: the glyph run plus its quiet negative-space padding
// (2 cells per side for the 58-column art → 62 per PLAN §3.2; 1 cell
// per side for the 19-cell compact lockup → 21 per PLAN §6.2).
func maskWidthFull(wm wordmarkMode) int {
	switch wm {
	case wordmarkCompact:
		return compactLockupCells + 2
	default:
		return asciiBannerWidth + 2*canvasMaskPad
	}
}

// bindBand resolves the draw window for this frame. The reveal sweeps
// outward from the intersection at radius R = ⌊p_w·half⌋ with
// half = ⌊maskFull/2⌋: the full mode is pair-based, sweeping
// [xc−R, xc+R−1] (the innermost pair is xc−1..xc; at R=31 the sweep
// equals exactly PLAN §3.2's mask [xc−31, xc+30]), while the compact
// odd-width mode is column-symmetric, |c−xc| ≤ R up to 10. The fade
// shrinks the mask the same way every frame: W_mask(α) = ⌊α·maskFull⌋
// (PLAN §5.2.1), centered on Xc and contracting back toward it
// (§4 phase 7), so underlying cells reclaim the window edge-first as α
// collapses. The drawn window is the intersection of the two; when it
// is empty nothing is spliced and the underlying row shows untouched.
func (g canvasGeometry) bindBand(anim *openingAnimState, wm wordmarkMode) wordmarkBand {
	maskFull := maskWidthFull(wm)

	b := wordmarkBand{
		glyphW:      asciiBannerWidth,
		glyphOrigin: g.xc - asciiBannerWidth/2,
		rows:        bannerArtRows,
	}
	if wm == wordmarkCompact {
		b.glyphW = compactLockupCells
		b.glyphOrigin = g.xc - compactLockupCells/2
		b.rows = 1
	}
	if anim == nil || !anim.Started() || anim.Done() {
		return b // never revealed: nothing drawn, nothing occluded
	}
	pw := anim.WordmarkRevealProgress()
	alpha := anim.WordmarkFadeAlpha()
	if pw <= 0 || alpha <= 0 {
		return b // reveal not begun, or fade finished and latched
	}

	half := maskFull / 2
	reveal := int(math.Floor(pw * float64(half)))
	maskW := int(math.Floor(alpha * float64(maskFull)))
	maskLeft := g.xc - maskW/2
	maskRight := maskLeft + maskW - 1

	sweepLeft := g.xc - reveal
	sweepRight := g.xc + reveal
	if wm != wordmarkCompact {
		sweepRight-- // pair-based sweep ends one cell left of symmetry
	}

	dl := max(sweepLeft, maskLeft)
	dr := min(sweepRight, maskRight)
	if dr < dl {
		return b
	}
	b.alpha = alpha
	b.dt = anim.elapsed
	b.active = true
	b.drawnLeft, b.drawnRight = dl, dr
	return b
}

// bandRowIdx maps canvas row y to its banner row (0-based) inside the
// active band, or −1 when the row carries no wordmark slice. The full
// band straddles rows yc−1..yc+1 with the middle art row over the
// separator row; the compact lockup draws on yc alone.
func bandRowIdx(g canvasGeometry, b wordmarkBand, y int) int {
	if y == g.yc {
		if b.rows == 1 {
			return 0
		}
		return 1
	}
	if b.rows == 3 {
		switch y {
		case g.yc - 1:
			return 0
		case g.yc + 1:
			return 2
		}
	}
	return -1
}

// assembleRow splices the band window into one grid row: the row is
// exact-width already (region pieces and separator pieces are), so the
// window is expressed as three escape-safe cell cuts — content left of
// the mask, the band cells, then content right of it. Rows that carry
// no slice this frame return unchanged.
func (g canvasGeometry) assembleRow(row string, b wordmarkBand, y int) string {
	br := bandRowIdx(g, b, y)
	if br < 0 {
		return row
	}
	cl := max(0, b.drawnLeft)
	cr := min(g.cW-1, b.drawnRight)
	if cr < cl {
		return row
	}
	var sb strings.Builder
	sb.Grow(g.cW)
	sb.WriteString(cutANSIWindow(row, 0, cl))
	sb.WriteString(bandRowCells(b, br, cl, cr))
	sb.WriteString(cutANSIWindow(row, cr+1, g.cW))
	return sb.String()
}

// bandRowCells renders the band cells for the window cl..cr of one
// banner row: glyph cells hit the art run, everything else in the
// window (the surrounding negative-space pads, or glyph positions cut
// off the canvas) is quiet blank space. The color profile is probed
// once per row so per-cell styling below never re-reads it.
func bandRowCells(b wordmarkBand, br, cl, cr int) string {
	profile := openingColorProfile()
	var ansi256Style lipgloss.Style
	if profile == termenv.ANSI256 {
		// Palette quantization drops the per-cell gradient phase; the
		// whole run reads as one ramp step this frame (PLAN §5.2.2).
		ansi256Style = lipgloss.NewStyle().Bold(true).Foreground(wordmarkANSI256(b.alpha))
	}
	fadeStyle := wordmarkFadeStyle(b.alpha)

	var sb strings.Builder
	sb.Grow(cr - cl + 1)
	for c := cl; c <= cr; c++ {
		bx := c - b.glyphOrigin
		if bx < 0 || bx >= b.glyphW {
			sb.WriteByte(' ')
			continue
		}
		r := bannerRune(b, br, bx)
		if r == ' ' {
			// Art holes stay quiet blanks; styling spaces would only
			// paint black-on-black escape noise.
			sb.WriteByte(' ')
			continue
		}
		switch profile {
		case termenv.TrueColor:
			// Per-cell gradient wave with the fade collapsing each
			// glyph toward black (PLAN §4 phase 4, §5.2.1).
			phi := wordmarkGradientPhase(bx, br, b.dt)
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(wordmarkTrueColor(phi, b.alpha)).Render(string(r)))
		case termenv.ANSI256:
			sb.WriteString(ansi256Style.Render(string(r)))
		default:
			// ANSI 16 / Ascii / NO_COLOR: glyph-density dissolve over
			// the fade window (PLAN §5.2.3).
			sb.WriteString(fadeStyle.Render(string(wordmarkFadeGlyph(r, b.alpha))))
		}
	}
	return sb.String()
}

// bannerRune returns the art rune at banner row br / glyph column bx,
// or a blank fallback for rows and columns with no art (padded edges
// and the single-row lockup on the far band columns).
func bannerRune(b wordmarkBand, br, bx int) rune {
	if b.rows == 1 {
		if bx < len(compactLockupRunes) {
			return compactLockupRunes[bx]
		}
		return ' '
	}
	if br < 0 || br >= len(canvasBannerRunes) {
		return ' '
	}
	line := canvasBannerRunes[br]
	if bx < 0 || bx >= len(line) {
		return ' '
	}
	return line[bx]
}

// endsInANSIReset reports whether s already terminates an SGR sequence
// with a full reset, covering both the \x1b[m (x/ansi) and \x1b[0m
// (termenv) encodings so cut guards avoid stacking redundant resets.
func endsInANSIReset(s string) bool {
	return strings.HasSuffix(s, ansi.ResetStyle) || strings.HasSuffix(s, "\x1b[0m")
}

// cutANSIWindow returns the cell window [left, right) of s, escape-safe:
// ANSI sequences ride along with their cells (openers from a skipped
// region are retained so visible text keeps its style), grapheme and
// wide-rune widths are respected, and a window that severs an open
// style run gets a trailing reset so nothing bleeds across the splice
// boundary.
func cutANSIWindow(s string, left, right int) string {
	if right <= left || s == "" {
		return ""
	}
	left = max(0, left)
	w := ansi.StringWidth(s)
	if left >= w {
		return ""
	}
	if left == 0 && right >= w {
		return s
	}
	out := ansi.Cut(s, left, right)
	if out == "" {
		return ""
	}
	// Close a severed style run so color can't bleed across the splice
	// boundary. Plain windows without any escape stay exactly as cut,
	// or Ascii-profile output would fill with bare reset noise.
	if strings.Contains(out, "\x1b") && !endsInANSIReset(out) {
		return out + ansi.ResetStyle
	}
	return out
}

// finalizeRow pins a row to exactly w display cells: short rows are
// blank-padded, overflowing rows are truncated by cell width (never by
// byte), and a truncation that severed an open style run gets a
// trailing reset. Every assembled row passes through here, keeping the
// compositor's width invariant backed by construction.
func finalizeRow(row string, w int) string {
	if w <= 0 {
		return ""
	}
	width := ansi.StringWidth(row)
	if width == w {
		return row
	}
	if width > w {
		out := ansi.Truncate(row, w, "")
		if strings.Contains(out, "\x1b") && out != "" && !endsInANSIReset(out) {
			return out + ansi.ResetStyle
		}
		return out
	}
	return row + strings.Repeat(" ", w-width)
}
