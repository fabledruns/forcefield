package tui

import (
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
	scope    pickerScope
	provider string // providerID this picker's models belong to; only set for scopeModel

	// heights caches how many terminal rows each option rendered to.
	// Options with a detail line occupy two rows; a detail too long for
	// the modal width may wrap further, so heights are measured from the
	// real rendering instead of assumed. Options never change after
	// construction, so the cache never goes stale.
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

// modelOptions builds the /model picker's rows for one provider from its
// known model IDs, resolving friendly display names where they exist.
func modelOptions(summaries []runtime.ProviderSummary, providerID, currentID string) []selectOption {
	var ids []string
	for _, s := range summaries {
		if s.ID == providerID {
			ids = s.Models
			break
		}
	}
	options := make([]selectOption, 0, len(ids))
	for _, id := range ids {
		options = append(options, selectOption{
			Label:   providers.ModelDisplayName(providerID, id),
			ID:      id,
			Current: id == currentID,
		})
	}
	return options
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
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, p.box())
}

// box renders just the modal (border, title, rows, help) without screen
// centering. Rendering and hit-testing share it: geometry helpers measure
// this exact string, so click targets can never drift from what is drawn.
func (p *selectPicker) box() string {
	var b strings.Builder

	b.WriteString(pickerTitleStyle.Render(p.title))
	b.WriteString("\n\n")

	for i, opt := range p.options {
		b.WriteString(p.renderRow(i, opt))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(pickerHelpStyle.Render("↑/↓ select · enter choose · esc cancel · click row"))

	return pickerBorderStyle.Width(pickerWidth).Render(strings.TrimRight(b.String(), "\n"))
}

// boxOrigin returns the screen coordinates of the modal box's top-left
// corner when centered by view. See sessionPicker.boxOrigin for row math.
func (p *selectPicker) boxOrigin(width, height int) (x, y int) {
	b := p.box()
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
// (label or detail line) selects it.
func (p *selectPicker) rowAt(x, y, width, height int) (int, bool) {
	bx, by := p.boxOrigin(width, height)
	offset := y - (by + selectRowsTop)
	if offset < 0 {
		return -1, false
	}
	start := 0
	for i, h := range p.optionHeights() {
		if offset < start+h {
			innerX := bx + 3
			if x < innerX-1 || x >= innerX+pickerWidth {
				return -1, false
			}
			return i, true
		}
		start += h
	}
	return -1, false
}

// renderRow formats a single option row: a selection cursor, the friendly
// label, a checkmark if it's the active choice, and — when present — a
// muted second line with the capability/availability detail.
func (p *selectPicker) renderRow(i int, opt selectOption) string {
	cursor := "  "
	if i == p.cursor {
		cursor = "› "
	}

	line := cursor + opt.Label
	if opt.Current {
		line += " " + pickerActiveStyle.Render("✓")
	}

	style := pickerMetaStyle
	if i == p.cursor {
		style = pickerSelectedStyle
	}

	if opt.Detail == "" {
		return style.Width(pickerWidth - 4).Render(line)
	}

	detailLine := "    " + pickerDetailStyle.Render(opt.Detail)
	body := style.Width(pickerWidth-4).Render(line) + "\n" +
		pickerMetaStyle.Width(pickerWidth-4).Render(detailLine)
	return body
}
