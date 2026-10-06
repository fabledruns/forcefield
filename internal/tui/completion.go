package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// maxSuggestions is the most commands ever shown in the palette at once.
const maxSuggestions = 5

// commandPrefix reports the text typed after "/" so far, and whether the
// input is currently positioned in a command name at all (as opposed to
// a plain chat message, or a command whose name is already finished and
// followed by arguments).
func (m model) commandPrefix() (prefix string, editingName bool) {
	value := m.input.Value()
	if !strings.HasPrefix(value, "/") {
		return "", false
	}
	body := value[1:]
	if strings.ContainsAny(body, " \t") {
		return "", false // past the command name, into its arguments
	}
	return body, true
}

// paletteShown reports how many palette rows are currently visible,
// capped at maxSuggestions. The cursor always ranges over these rows.
func (m model) paletteShown() int {
	if len(m.suggestions) > maxSuggestions {
		return maxSuggestions
	}
	return len(m.suggestions)
}

// paletteOpen reports whether the command palette is visible: the input
// holds an in-progress command name with at least one match.
func (m model) paletteOpen() bool {
	return m.paletteShown() > 0
}

// updateSuggestions recomputes the palette from the current input text.
// Typing filters via the same lookup Tab-completion uses
// (m.registry.Match), and any keystroke resets the active row to the top.
func (m *model) updateSuggestions() {
	prefix, editingName := m.commandPrefix()
	if !editingName {
		m.suggestions = nil
		m.suggestionCursor = 0
		return
	}
	if _, ok := m.registry.Lookup(strings.ToLower(prefix)); ok {
		// Typed text already names a real command (or alias) exactly;
		// nothing left to filter.
		m.suggestions = nil
		m.suggestionCursor = 0
		return
	}
	m.suggestions = m.registry.Match(strings.ToLower(prefix))
	m.suggestionCursor = 0
}

// movePalette moves the active row, wrapping around at the ends like
// Crush's command dialog. It is a no-op while the palette is closed.
func (m *model) movePalette(delta int) {
	shown := m.paletteShown()
	if shown == 0 {
		return
	}
	m.suggestionCursor = ((m.suggestionCursor+delta)%shown + shown) % shown
}

// selectPalette completes the input with the palette row at index,
// exactly as choosing it via mouse would. The palette then re-filters:
// an exact command name hides it, leaving the user free to add arguments
// or press Enter again to run.
func (m *model) selectPalette(idx int) {
	if idx < 0 || idx >= m.paletteShown() {
		return
	}
	m.input.SetValue("/" + m.suggestions[idx].Name())
	m.input.CursorEnd()
	m.updateSuggestions()
	m.layout()
}

// selectPaletteActive completes the input with the highlighted row.
func (m *model) selectPaletteActive() {
	m.selectPalette(m.suggestionCursor)
}

// handleTabComplete selects the highlighted palette row. Tab never cycles
// anymore: filtering is live on every keystroke and arrows move the
// highlight, so Tab is just another way to confirm it.
func (m model) handleTabComplete() (tea.Model, tea.Cmd) {
	if !m.paletteOpen() {
		return m, nil
	}
	m.selectPaletteActive()
	return m, nil
}

// renderSuggestions draws the command palette: one compact row per match,
// each with its one-line description. The active row's command name uses
// Forcefield pink; everything else stays quiet. Returns "" when closed,
// so callers can drop it without a stray blank line.
func (m model) renderSuggestions() string {
	shown := m.paletteShown()
	if shown == 0 {
		return ""
	}

	rows := make([]string, 0, shown)
	for i := 0; i < shown; i++ {
		rows = append(rows, m.renderPaletteRow(i))
	}
	return strings.Join(rows, "\n")
}

// renderPaletteRow draws one palette row: a marker plus "/name" plus the
// command's description, truncated to the terminal width so rows never
// wrap and hit geometry stays one-row-per-command.
func (m model) renderPaletteRow(i int) string {
	cmd := m.suggestions[i]
	name := "/" + cmd.Name()
	desc := strings.ReplaceAll(cmd.Description(), "\n", " ")

	const markerActive = "› "
	const markerIdle = "  "
	const gap = "  "
	budget := m.width - lipgloss.Width(markerActive) - lipgloss.Width(name) - lipgloss.Width(gap)
	if budget < 0 {
		budget = 0
	}
	desc = truncateCells(desc, budget)

	if i == m.suggestionCursor {
		return paletteActiveStyle.Render(markerActive+name) + gap + paletteDescStyle.Render(desc)
	}
	return markerIdle + paletteNameStyle.Render(name) + gap + paletteDescStyle.Render(desc)
}
