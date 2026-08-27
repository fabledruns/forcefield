package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// pickerScope distinguishes what a selectPicker is choosing between:
// providers, or the models belonging to one provider.
type pickerScope int

const (
	scopeProvider pickerScope = iota
	scopeModel
)

// selectOption is one row in a selectPicker: a friendly label, the real
// ID to switch to, an optional muted detail line (capabilities,
// availability), and whether it's the currently active choice.
type selectOption struct {
	Label   string
	ID      string
	Detail  string
	Current bool
}

// selectPicker is a small, self-contained component for choosing a
// provider or a model by friendly name, mirroring sessionPicker: it
// holds a snapshot of its options and a cursor, and never switches
// anything itself — the caller reads Selected() once Enter is pressed.
type selectPicker struct {
	title    string
	options  []selectOption
	cursor   int
	offset   int       // first visible option index, for vertical scrolling
	scope    pickerScope
	provider string // providerID this picker's models belong to; only set for scopeModel

	// fetching is true while this provider's model list is being
	// discovered in the background; status holds a concise outcome line
	// (e.g. why discovery failed). Both render below the option rows and
	// never affect hit-testing.
	fetching bool
	status   string

	// heights caches how many terminal rows each option rendered to.
	// Options with a detail line occupy two rows; a detail too long for
	// the modal width is truncated rather than wrapped, so heights are
	// stable (1 or 2). Options never change after construction, so the
	// cache never goes stale.
	heights []int
}

// newSelectPicker builds a picker over the given options. Options may
// carry detail lines; rows with details render taller and hit-testing
// follows the real rendered geometry.
func newSelectPicker(title string, options []selectOption, scope pickerScope) *selectPicker {
	cursor := 0
	for i, opt := range options {
		if opt.Current {
			cursor = i
		}
	}
	return &selectPicker{title: title, options: options, cursor: cursor, scope: scope}
}

// providerOptions builds the /provider picker's rows from the runtime's
// provider summaries: display name plus a local/cloud · capabilities ·
// availability detail line.
func providerOptions(summaries []runtime.ProviderSummary, currentID string) []selectOption {
	options := make([]selectOption, 0, len(summaries))
	for _, s := range summaries {
		options = append(options, selectOption{
			Label:   s.Name,
			ID:      s.ID,
			Detail:  s.Detail,
			Current: s.ID == currentID,
		})
	}
	return options
}

// modelOptions builds the /model picker's rows for one provider from the
// generic model metadata the runtime assembled (active model first, then
// discovered or fallback entries). A "Refresh models" row is appended
// unless discovery is unsupported for this transport.
func modelOptions(models []providers.Model, currentID string, state runtime.ModelListState) []selectOption {
	options := make([]selectOption, 0, len(models)+1)
	for _, m := range models {
		options = append(options, selectOption{
			Label:   m.Name,
			ID:      m.ID,
			Detail:  modelDetail(m),
			Current: m.ID == currentID,
		})
	}
	if state != runtime.ModelsUnsupported {
		options = append(options, selectOption{Label: "↻ Refresh models", ID: refreshOptionID})
	}
	return options
}

// modelDetail renders the per-model detail line from facts the provider
// actually reported. Unknown context windows stay silent rather than
// guessed.
func modelDetail(m providers.Model) string {
	switch {
	case m.ContextWindow > 0:
		return fmt.Sprintf("context %s", humanizeTokens(m.ContextWindow))
	default:
		return ""
	}
}

// humanizeTokens renders a token count compactly ("128k", "1m").
func humanizeTokens(n int64) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return fmt.Sprintf("%dm", n/1_000_000)
	case n >= 1000 && n%1000 == 0:
		return fmt.Sprintf("%dk", n/1000)
	case n > 0:
		return strconv.FormatInt(n, 10)
	default:
		return ""
	}
}

// maxVisibleRows returns how many terminal rows the picker can devote
// to option rows given the terminal height. It reserves space for the
// border, title, help and optional fetching/status lines so the modal
// never overflows the screen.
func (p *selectPicker) maxVisibleRows(termHeight int) int {
	if termHeight <= 0 {
		return 1000 // tests that call box() without a height see all rows
	}
	// chrome: border(2) + padding(2) + title(1) + blank(1) + blank before help(1) + help(1) = 8
	chrome := 8
	if p.fetching || p.status != "" {
		chrome += 3 // blank + status line + blank (see box)
	}
	avail := termHeight - chrome - 2 // 2 for outer margin when centered
	if avail < 5 {
		avail = 5
	}
	if avail > 15 {
		avail = 15
	}
	return avail
}

// visibleRange returns the slice [start,end) of options that fit in
// maxRows terminal rows starting from p.offset. Heights are measured from
// the actual rendered rows (1 or 2 per option) so a detail line counts.
func (p *selectPicker) visibleRange(maxRows int) (int, int) {
	if len(p.options) == 0 {
		return 0, 0
	}
	heights := p.optionHeights()
	start := p.offset
	if start < 0 {
		start = 0
	}
	if start >= len(p.options) {
		start = len(p.options) - 1
	}
	rows := 0
	end := start
	for end < len(p.options) && rows+heights[end] <= maxRows {
		rows += heights[end]
		end++
	}
	if end == start && start < len(p.options) {
		end = start + 1 // always show at least one, even if it exceeds maxRows slightly
	}
	return start, end
}

// ensureVisible adjusts p.offset so the cursor is within the visible
// window for the given terminal height. It is idempotent and safe to call
// before every render or hit-test.
func (p *selectPicker) ensureVisible(termHeight int) {
	if len(p.options) == 0 {
		p.offset = 0
		return
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.options) {
		p.cursor = len(p.options) - 1
	}
	if p.cursor < p.offset {
		p.offset = p.cursor
		return
	}
	maxRows := p.maxVisibleRows(termHeight)
	_, end := p.visibleRange(maxRows)
	// Cursor beyond visible window: bump offset forward until it fits.
	for p.cursor >= end && p.offset < len(p.options) {
		p.offset++
		_, end = p.visibleRange(maxRows)
		// guard against infinite loop if heights mis-measured
		if p.offset > p.cursor {
			break
		}
	}
}

// moveUp/moveDown move the selection cursor, clamped to the list bounds.
func (p *selectPicker) moveUp() {
	if p.cursor > 0 {
		p.cursor--
	}
}

func (p *selectPicker) moveDown() {
	if p.cursor < len(p.options)-1 {
		p.cursor++
	}
}

// selected returns the option currently under the cursor.
func (p *selectPicker) selected() selectOption {
	return p.options[p.cursor]
}

// view renders the picker as a centered modal over a width x height area.
func (p *selectPicker) view(width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, p.boxForHeight(height))
}

// box renders just the modal without height clipping. It exists for
// tests that call box() directly without a terminal size; production code
// goes through boxForHeight which caps the visible window.
func (p *selectPicker) box() string {
	return p.boxForHeight(1000)
}

// boxForHeight renders the modal limited to termHeight rows. Only the
// visible slice of options is drawn, with scroll indicators when the
// list is longer than the available space. Rendering and hit-testing
// share this exact string so click targets never drift from what is drawn.
func (p *selectPicker) boxForHeight(termHeight int) string {
	p.ensureVisible(termHeight)

	var b strings.Builder

	b.WriteString(pickerTitleStyle.Render(p.title))
	b.WriteString("\n\n")

	innerW := pickerWidth - 4
	maxRows := p.maxVisibleRows(termHeight)
	start, end := p.visibleRange(maxRows)

	if start > 0 {
		b.WriteString(pickerMetaStyle.Width(innerW).Render(fmt.Sprintf("  ↑ %d more above", start)))
		b.WriteString("\n")
	}

	for i := start; i < end; i++ {
		b.WriteString(p.renderRow(i, p.options[i]))
		b.WriteString("\n")
	}

	if end < len(p.options) {
		b.WriteString(pickerMetaStyle.Width(innerW).Render(fmt.Sprintf("  ↓ %d more", len(p.options)-end)))
		b.WriteString("\n")
	}

	if p.fetching {
		b.WriteString("\n")
		b.WriteString(pickerMetaStyle.Render("Fetching models…"))
		b.WriteString("\n")
	} else if p.status != "" {
		b.WriteString("\n")
		b.WriteString(pickerMetaStyle.Render(p.status))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	help := "↑/↓ select · enter choose · esc cancel · click row"
	if p.scope == scopeModel && !p.fetching {
		help = "↑/↓ select · enter choose · r refresh · esc cancel"
	}
	b.WriteString(pickerHelpStyle.Render(help))

	return pickerBorderStyle.Width(pickerWidth).Render(strings.TrimRight(b.String(), "\n"))
}

// boxOrigin returns the screen coordinates of the modal box's top-left
// corner when centered by view. See sessionPicker.boxOrigin for row math.
func (p *selectPicker) boxOrigin(width, height int) (x, y int) {
	b := p.boxForHeight(height)
	x = max(0, (width-lipgloss.Width(b))/2)
	y = max(0, (height-lipgloss.Height(b))/2)
	return x, y
}

// rowsTop is how many rows separate the box's top edge from its first
// option row (border + padding + title + blank line). It must match box().
const selectRowsTop = pickerRowsTop

// optionHeights measures (once) and returns each option's rendered row
// count. Measuring the real rendering keeps click targets aligned even
// when a detail line wraps.
func (p *selectPicker) optionHeights() []int {
	if p.heights == nil || len(p.heights) != len(p.options) {
		p.heights = make([]int, len(p.options))
		for i, opt := range p.options {
			p.heights[i] = lipgloss.Height(p.renderRow(i, opt))
		}
	}
	return p.heights
}

// rowAt resolves a point to an option index inside the modal. Each
// option occupies its measured band of rows; clicking anywhere inside it
// (label or detail line) selects it. Only the currently visible slice is
// hit-testable; scroll indicators and chrome are never selectable.
func (p *selectPicker) rowAt(x, y, width, height int) (int, bool) {
	p.ensureVisible(height)
	bx, by := p.boxOrigin(width, height)
	offset := y - (by + selectRowsTop)
	if offset < 0 {
		return -1, false
	}

	maxRows := p.maxVisibleRows(height)
	start, end := p.visibleRange(maxRows)

	// Account for the optional "more above" indicator row.
	if start > 0 {
		if offset == 0 {
			return -1, false
		}
		offset--
	}

	heights := p.optionHeights()
	pos := 0
	for i := start; i < end; i++ {
		h := heights[i]
		if offset < pos+h {
			innerX := bx + 3
			if x < innerX-1 || x >= innerX+pickerWidth {
				return -1, false
			}
			return i, true
		}
		pos += h
	}
	return -1, false
}

// renderRow formats a single option row: a selection cursor, the friendly
// label, a checkmark if it's the active choice, and — when present — a
// muted second line with the capability/availability detail. Labels and
// details are truncated with an ellipsis to the inner width so a very long
// model name never wraps and never blows up the modal's height.
func (p *selectPicker) renderRow(i int, opt selectOption) string {
	cursor := "  "
	if i == p.cursor {
		cursor = "› "
	}

	innerW := pickerWidth - 4
	cursorW := lipgloss.Width(cursor)
	checkW := 0
	if opt.Current {
		checkW = lipgloss.Width(" ✓")
	}
	maxLabelW := innerW - cursorW - checkW
	if maxLabelW < 4 {
		maxLabelW = 4
	}
	truncLabel := truncateCells(opt.Label, maxLabelW)

	line := cursor + truncLabel
	if opt.Current {
		line += " " + pickerActiveStyle.Render("✓")
	}

	style := pickerMetaStyle
	if i == p.cursor {
		style = pickerSelectedStyle
	}

	if opt.Detail == "" {
		return style.Width(innerW).Render(line)
	}

	maxDetailW := innerW - 4 // indent
	truncDetail := truncateCells(opt.Detail, maxDetailW)
	detailLine := "    " + pickerDetailStyle.Render(truncDetail)
	body := style.Width(innerW).Render(line) + "\n" +
		pickerMetaStyle.Width(innerW).Render(detailLine)
	return body
}
