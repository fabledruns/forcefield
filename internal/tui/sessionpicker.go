package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/command/builtin"
	"forcefield/internal/session"
)

// pickerWidth is the fixed content width of the session picker modal,
// independent of terminal size, so the list stays readable even in a
// very wide window.
const pickerWidth = 60

// maxShortID is how many characters of a session's UUID are shown in
// the picker; enough to disambiguate at a glance without cluttering
// the row.
const maxShortID = 8

// sessionPicker is a small, self-contained component: it holds the
// list of sessions loaded once when the modal opened, and a cursor
// into it. It never reads from disk and never mutates the active
// session — Enter is reported by the caller reading Selected() after
// Update returns done == true.
type sessionPicker struct {
	sessions []session.Session
	cursor   int
	activeID string
}

// newSessionPicker builds a picker over an already-loaded, already-sorted
// list of sessions. activeID highlights the currently active session.
func newSessionPicker(sessions []session.Session, activeID string) *sessionPicker {
	cursor := 0
	for i, s := range sessions {
		if s.ID == activeID {
			cursor = i
			break
		}
	}
	return &sessionPicker{sessions: sessions, activeID: activeID, cursor: cursor}
}

// moveUp/moveDown move the selection cursor, clamped to the list bounds.
func (p *sessionPicker) moveUp() {
	if p.cursor > 0 {
		p.cursor--
	}
}

func (p *sessionPicker) moveDown() {
	if p.cursor < len(p.sessions)-1 {
		p.cursor++
	}
}

// selected returns the session currently under the cursor.
func (p *sessionPicker) selected() session.Session {
	return p.sessions[p.cursor]
}

// view renders the picker as a centered modal over a width x height area.
func (p *sessionPicker) view(width, height int) string {
	var b strings.Builder

	b.WriteString(pickerTitleStyle.Render("Sessions"))
	b.WriteString("\n\n")

	for i, sess := range p.sessions {
		b.WriteString(p.renderRow(i, sess))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(pickerHelpStyle.Render("↑/↓ select · enter switch · esc/q close"))

	box := pickerBorderStyle.Width(pickerWidth).Render(strings.TrimRight(b.String(), "\n"))

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// renderRow formats a single session row: a selection cursor, an active
// marker, the preview title, the shortened ID, and the last-updated time.
func (p *sessionPicker) renderRow(i int, sess session.Session) string {
	cursor := "  "
	if i == p.cursor {
		cursor = "› "
	}

	active := "  "
	if sess.ID == p.activeID {
		active = pickerActiveStyle.Render("●") + " "
	}

	shortID := sess.ID
	if len(shortID) > maxShortID {
		shortID = shortID[:maxShortID]
	}

	line := fmt.Sprintf(
		"%s%s%s  (%s · %s)",
		cursor,
		active,
		builtin.SessionTitle(sess),
		shortID,
		builtin.FormatTime(sess.UpdatedAt),
	)

	if i == p.cursor {
		return pickerSelectedStyle.Width(pickerWidth - 4).Render(line)
	}
	return pickerMetaStyle.Width(pickerWidth - 4).Render(line)
}