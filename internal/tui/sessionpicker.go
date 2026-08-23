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
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, p.box())
}

// box renders just the modal (border, title, rows, help) without screen
// centering. Rendering and hit-testing share it: geometry helpers measure
// this exact string, so click targets can never drift from what is drawn.
func (p *sessionPicker) box() string {
	var b strings.Builder

	b.WriteString(pickerTitleStyle.Render("Sessions"))
	b.WriteString("\n\n")

	for i, sess := range p.sessions {
		b.WriteString(p.renderRow(i, sess))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(pickerHelpStyle.Render("↑/↓ select · enter switch · esc/q close · click row"))

	return pickerBorderStyle.Width(pickerWidth).Render(strings.TrimRight(b.String(), "\n"))
}

// boxOrigin returns the screen coordinates of the modal box's top-left
// corner when centered by view. Rows begin after top border+padding and
// the title plus blank line; each occupies exactly one row.
func (p *sessionPicker) boxOrigin(width, height int) (x, y int) {
	b := p.box()
	x = max(0, (width-lipgloss.Width(b))/2)
	y = max(0, (height-lipgloss.Height(b))/2)
	return x, y
}

// pickerRowsTop is how many rows separate the box's top edge from its
// first option row: border (1) + padding (1) + title (1) + blank (1).
const pickerRowsTop = 4

// rowAt resolves a point to an option-row index inside the modal.
func (p *sessionPicker) rowAt(x, y, width, height int) (int, bool) {
	bx, by := p.boxOrigin(width, height)
	first := by + pickerRowsTop
	if y < first || y >= first+len(p.sessions) {
		return -1, false
	}
	innerX := bx + 1 /*border*/ + 2 /*padding*/
	if x < innerX-1 || x >= innerX+pickerWidth {
		return -1, false
	}
	return y - first, true
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
