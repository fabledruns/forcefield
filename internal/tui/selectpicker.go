package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/providers"
)

// pickerScope distinguishes what a selectPicker is choosing between:
// providers, or the models belonging to one provider.
type pickerScope int

const (
	scopeProvider pickerScope = iota
	scopeModel
)

// selectOption is one row in a selectPicker: a friendly label, the real
// ID to switch to, and whether it's the currently active choice.
type selectOption struct {
	Label   string
	ID      string
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
}

// newProviderPicker builds a picker over every registered provider,
// with currentID's row highlighted as the active choice.
func newProviderPicker(currentID string) *selectPicker {
	options := make([]selectOption, len(providers.Registry))
	cursor := 0
	for i, p := range providers.Registry {
		options[i] = selectOption{Label: p.Name, ID: p.ID, Current: p.ID == currentID}
		if options[i].Current {
			cursor = i
		}
	}
	return &selectPicker{title: "Provider", options: options, cursor: cursor, scope: scopeProvider}
}

// newModelPicker builds a picker over the models registered for
// providerID, with currentID's row highlighted as the active choice. If
// providerID isn't registered, the picker has no options.
func newModelPicker(providerID, currentID string) *selectPicker {
	p, ok := providers.ByID(providerID)
	if !ok {
		return &selectPicker{title: "Model", scope: scopeModel, provider: providerID}
	}

	options := make([]selectOption, len(p.Models))
	cursor := 0
	for i, mi := range p.Models {
		options[i] = selectOption{Label: mi.Name, ID: mi.ID, Current: mi.ID == currentID}
		if options[i].Current {
			cursor = i
		}
	}
	return &selectPicker{title: "Model", options: options, cursor: cursor, scope: scopeModel, provider: providerID}
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
	var b strings.Builder

	b.WriteString(pickerTitleStyle.Render(p.title))
	b.WriteString("\n\n")

	for i, opt := range p.options {
		b.WriteString(p.renderRow(i, opt))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(pickerHelpStyle.Render("↑/↓ select · enter choose · esc cancel"))

	box := pickerBorderStyle.Width(pickerWidth).Render(strings.TrimRight(b.String(), "\n"))

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// renderRow formats a single option row: a selection cursor, the
// friendly label, and a checkmark if it's the active choice.
func (p *selectPicker) renderRow(i int, opt selectOption) string {
	cursor := "  "
	if i == p.cursor {
		cursor = "› "
	}

	line := cursor + opt.Label
	if opt.Current {
		line += " " + pickerActiveStyle.Render("✓")
	}

	if i == p.cursor {
		return pickerSelectedStyle.Width(pickerWidth - 4).Render(line)
	}
	return pickerMetaStyle.Width(pickerWidth - 4).Render(line)
}
