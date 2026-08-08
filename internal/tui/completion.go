package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// maxSuggestions is the most commands ever shown in the live suggestion
// list at once, per the UX spec ("max 5 items").
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

// updateSuggestions recomputes the live suggestion list from the current
// input text. Reused as-is instead of duplicating command names: it's
// just m.registry.Match, the same lookup Tab-completion uses.
func (m *model) updateSuggestions() {
	prefix, editingName := m.commandPrefix()
	if !editingName {
		m.suggestions = nil
		return
	}
	if _, ok := m.registry.Lookup(strings.ToLower(prefix)); ok {
		// Typed text already names a real command (or alias) exactly;
		// nothing left to suggest.
		m.suggestions = nil
		return
	}
	m.suggestions = m.registry.Match(strings.ToLower(prefix))
}

// handleTabComplete implements Tab-to-autocomplete: a lone match
// completes immediately, and repeated Tab presses cycle through several
// matches for the same prefix.
func (m model) handleTabComplete() (tea.Model, tea.Cmd) {
	prefix, editingName := m.commandPrefix()
	if !editingName {
		return m, nil
	}

	// A Tab press continues the previous cycle only if the input still
	// holds exactly the candidate that cycle last produced; anything
	// else (fresh typing, a deletion, ...) starts a new cycle from
	// whatever prefix is typed now.
	continuing := len(m.tabMatches) > 0 && prefix == m.tabMatches[m.tabIndex]

	if continuing {
		m.tabIndex = (m.tabIndex + 1) % len(m.tabMatches)
	} else {
		matches := m.registry.Match(strings.ToLower(prefix))
		if len(matches) == 0 {
			return m, nil
		}
		names := make([]string, len(matches))
		for i, c := range matches {
			names[i] = c.Name()
		}
		m.tabMatches = names
		m.tabIndex = 0
	}

	m.input.SetValue("/" + m.tabMatches[m.tabIndex])
	m.input.CursorEnd()
	m.updateSuggestions()
	return m, nil
}

// renderSuggestions draws the live suggestion list plus, for the top
// match, its one-line description. Returns "" when there's nothing to
// show, so callers can drop it without a stray blank line.
func (m model) renderSuggestions() string {
	if len(m.suggestions) == 0 {
		return ""
	}

	shown := m.suggestions
	if len(shown) > maxSuggestions {
		shown = shown[:maxSuggestions]
	}

	names := make([]string, len(shown))
	for i, c := range shown {
		names[i] = "/" + c.Name()
	}
	list := suggestionListStyle.Render(strings.Join(names, "   "))

	preview := suggestionPreviewStyle.Render(shown[0].Description())
	return lipgloss.JoinVertical(lipgloss.Left, list, preview)
}
