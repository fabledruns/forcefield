package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// These tests pin mouse geometry to the FULL rendered screen (model.View),
// the strongest ground truth available: whatever chrome (header, footer,
// borders) contributes above or below the viewport, a click on the row
// where content is visibly drawn must resolve to that content. A stale
// chrome constant once shifted every target one row down; only comparing
// against View output catches that class of bug.

func sizedReadyModel(t *testing.T) model {
	t.Helper()
	m := visibleBlocksModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(model)
}

func drawnRowFor(t *testing.T, m model, needle string) int {
	t.Helper()
	lines := strings.Split(stripANSI(m.View()), "\n")
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return i
		}
	}
	t.Fatalf("%q not found in rendered view", needle)
	return -1
}

func TestClicksResolveAtRowsDrawnInView(t *testing.T) {
	m := sizedReadyModel(t)

	targets := []struct {
		needle string
		entry  int
	}{
		{"first tool", indexOfToolEntry(m, "first tool")},
		{"second tool", indexOfToolEntry(m, "second tool")},
	}
	for _, tc := range targets {
		row := drawnRowFor(t, m, tc.needle)

		next, consumed := m.routeMouse(leftClick(3, row))
		m = next
		if !consumed {
			t.Fatalf("click on drawn row %d (%s) was not consumed", row, tc.needle)
		}
		if !m.entries[tc.entry].Tool.expanded {
			t.Fatalf("clicking %s at its drawn row %d did not expand it", tc.needle, row)
		}

		next, _ = m.routeMouse(leftClick(3, row)) // collapse for the next case
		m = next
		if m.entries[tc.entry].Tool.expanded {
			t.Fatalf("second click did not collapse %s", tc.needle)
		}
	}
}

func TestHoverResolvesAtRowsDrawnInView(t *testing.T) {
	m := sizedReadyModel(t)

	for _, s := range m.spans {
		needle := "Thinking"
		if s.action == actionToggleTool {
			needle = m.entries[s.entry].Content
		}
		row := drawnRowFor(t, m, needle)

		next, _ := m.routeMouse(tea.MouseMsg{X: 2, Y: row, Action: tea.MouseActionMotion})
		got := next.hoverID
		if got != s.id {
			t.Errorf("hover on drawn row %d = %q, want %q", row, got, s.id)
		}
	}
}

// The header must contribute exactly as many rows as hit-testing assumes:
// renderHeader's measured height drives both layout and mapping.
func TestHeaderRowsMatchesRenderedChrome(t *testing.T) {
	m := sizedReadyModel(t)

	// The first transcript entry renders as two rows: its "You" label,
	// then the body. The LABEL must sit exactly one row below the header.
	firstLabelRow := drawnRowFor(t, m, "You")

	if got := firstLabelRow; got != m.headerRows() {
		t.Fatalf("first transcript label draws at row %d, but headerRows() = %d", got, m.headerRows())
	}

	// And layout sizes the viewport so header + viewport + footer fit.
	total := m.headerRows() + m.viewport.Height + m.footerHeight()
	if total > m.height {
		t.Errorf("chrome overflows terminal: header(%d)+viewport(%d)+footer(%d) > %d",
			m.headerRows(), m.viewport.Height, m.footerHeight(), m.height)
	}
}

// Scrolled state must keep alignment: after wheeling up, clicks still land
// exactly where blocks are drawn.
func TestClickAlignmentSurvivesScrolling(t *testing.T) {
	m := longTranscriptModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(model)
	m.following = true
	m.viewport.GotoBottom()

	routed, consumed := m.routeMouse(wheelMsg(40, m.headerRows()+2, true))
	m = routed
	if !consumed || m.viewport.YOffset == 0 {
		t.Fatal("setup: did not scroll")
	}

	// Every visible span must be clickable at its drawn position.
	for _, s := range m.spans {
		screenY := m.headerRows() + s.startLine - m.viewport.YOffset
		if screenY < m.headerRows() || screenY >= m.headerRows()+m.viewport.Height {
			continue // scrolled off-screen
		}
		before := false
		switch s.action {
		case actionToggleTool:
			before = m.entries[s.entry].Tool.expanded
		case actionToggleThinking:
			before = m.entries[s.entry].Thinking.expanded
		}
		next, consumed := m.routeMouse(leftClick(3, screenY))
		m = next
		if !consumed {
			t.Fatalf("span %s visible at y=%d but click fell through", s.id, screenY)
		}
		var after bool
		switch s.action {
		case actionToggleTool:
			after = m.entries[s.entry].Tool.expanded
		case actionToggleThinking:
			after = m.entries[s.entry].Thinking.expanded
		}
		if after == before {
			t.Errorf("click at mapped row for %s (y=%d) did not toggle", s.id, screenY)
		}
		next, _ = m.routeMouse(leftClick(3, screenY))
		m = next
	}
}
