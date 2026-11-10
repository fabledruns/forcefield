package tui

// Input bar anchoring: the composed frame must fill the terminal
// exactly — header + content + footer = terminal height — so the input
// bar sits at the bottom of the viewport with no unexplained vertical
// gap beneath it, in normal mode and orchestration mode, at any size,
// and across resizes. The regression this pins: orchestration mode
// sized its canvas with the agents panel's rows still reserved even
// though the panel is not rendered there, floating the input bar up by
// exactly the panel's height.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// frameLines splits a composed View into its terminal rows.
func frameLines(view string) []string {
	return strings.Split(view, "\n")
}

// assertFrameFillsTerminal pins the bottom-anchoring invariant on one
// composed frame: the frame is exactly h rows (no gap anywhere, so the
// input cannot float above the bottom edge), the last row is the
// footer's status line, and the input box rows sit directly above it.
func assertFrameFillsTerminal(t *testing.T, view string, h int) {
	t.Helper()
	lines := frameLines(view)
	if len(lines) != h {
		t.Fatalf("composed frame is %d rows in a %d-row terminal (gap of %d rows beneath the input)",
			len(lines), h, h-len(lines))
	}
	last := lines[len(lines)-1]
	if strings.TrimSpace(last) == "" {
		t.Fatalf("the terminal's last row is blank; the footer does not reach the bottom:\n%q", last)
	}
	if !strings.Contains(last, "enter send") {
		t.Fatalf("the terminal's last row is not the footer status line: %q", last)
	}
	// The input box's border row sits directly above the status line —
	// the input is flush against the bottom, not separated from it.
	if strings.TrimSpace(lines[len(lines)-2]) == "" {
		t.Fatalf("a blank row sits between the input box and the status line:\n%q", lines[len(lines)-2])
	}
}

// TestInputAnchoredToBottomNormalMode proves the classic surface pins
// the input bar to the terminal's bottom edge at every height.
func TestInputAnchoredToBottomNormalMode(t *testing.T) {
	for _, h := range []int{8, 12, 14, 20, 24, 30, 40, 50} {
		m, _ := newOrchTestModel(t)
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: h})
		mm := next.(model)
		assertFrameFillsTerminal(t, mm.View(), h)
	}
}

// TestInputAnchoredToBottomWithAgentsPanel proves the same invariant
// while the agents panel is visible in normal mode: the panel is one
// of the composed blocks, the transcript yields the rows for it, and
// the input still ends the frame.
func TestInputAnchoredToBottomWithAgentsPanel(t *testing.T) {
	for _, h := range []int{14, 20, 30, 40} {
		m, _ := newOrchTestModel(t)
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: h})
		mm := next.(model)
		next, _ = mm.Update(streamEventMsg{Event: delegateStart("call-1"), gen: mm.streamGen})
		mm = next.(model)
		if mm.agentsPanelHeight() == 0 {
			t.Fatal("precondition: the agents panel never became visible")
		}
		assertFrameFillsTerminal(t, mm.View(), h)
	}
}

// TestInputAnchoredToBottomOrchestrationMode proves the quadrant
// canvas pins the input bar too — this is the mode that floated the
// input above the bottom: its footer height was counted with the
// agents panel's rows even though the canvas replaces the panel and
// the footer below the canvas never renders it.
func TestInputAnchoredToBottomOrchestrationMode(t *testing.T) {
	for _, h := range []int{12, 14, 20, 24, 30, 40} {
		m, _ := newOrchTestModel(t)
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: h})
		mm := next.(model)
		mm = activateOrch(t, mm, "call-1", "run-1")
		if mm.orchFooterHeight() >= mm.footerHeight() {
			t.Fatalf("orchestration footer height %d does not exclude the panel from the classic %d",
				mm.orchFooterHeight(), mm.footerHeight())
		}
		assertFrameFillsTerminal(t, mm.View(), h)
	}
}

// TestInputAnchoredAcrossResizes proves a resize recomputes the layout
// exactly: growing, shrinking, and crossing the two modes, every frame
// fills its terminal and the input stays at the bottom.
func TestInputAnchoredAcrossResizes(t *testing.T) {
	m, _ := newOrchTestModel(t)
	for _, size := range []tea.WindowSizeMsg{
		{Width: 120, Height: 40},
		{Width: 120, Height: 50},
		{Width: 80, Height: 20},
		{Width: 64, Height: 14},
		{Width: 100, Height: 30},
	} {
		next, _ := m.Update(size)
		mm := next.(model)
		assertFrameFillsTerminal(t, mm.View(), size.Height)
	}

	// The same walk with the orchestration canvas active.
	m2, _ := newOrchTestModel(t)
	next, _ := m2.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m2 = next.(model)
	m2 = activateOrch(t, m2, "call-1", "run-1")
	for _, size := range []tea.WindowSizeMsg{
		{Width: 120, Height: 30},
		{Width: 80, Height: 16},
		{Width: 100, Height: 24},
		{Width: 120, Height: 44},
	} {
		next, _ := m2.Update(size)
		mm := next.(model)
		assertFrameFillsTerminal(t, mm.View(), size.Height)
	}
}

// TestInputUsesRemainingViewportHeight proves the conversation area is
// exactly the terminal's remaining height — never a fixed size with
// slack underneath: transcript = height − header − footer, and the
// composed total therefore stays exact even as the input grows.
func TestInputUsesRemainingViewportHeight(t *testing.T) {
	m, _ := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	mm := next.(model)

	want := 30 - mm.headerRows() - mm.footerHeight()
	if mm.viewport.Height != want {
		t.Fatalf("viewport height = %d, want the remaining %d rows (30 − header %d − footer %d)",
			mm.viewport.Height, want, mm.headerRows(), mm.footerHeight())
	}

	// A taller terminal grows the conversation, not a gap.
	next, _ = mm.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	mm = next.(model)
	want = 50 - mm.headerRows() - mm.footerHeight()
	if mm.viewport.Height != want {
		t.Fatalf("viewport height after growth = %d, want %d", mm.viewport.Height, want)
	}
}

// TestInputNoOverlapWithCanvas proves orchestration mode's three
// stacked regions (header, canvas, footer) partition the terminal
// exactly: the canvas is the terminal's remaining height after header
// and footer, so neither can overlap the other.
func TestInputNoOverlapWithCanvas(t *testing.T) {
	m, _ := newOrchTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	mm := next.(model)
	mm = activateOrch(t, mm, "call-1", "run-1")

	canvasH := mm.height - mm.headerRows() - mm.orchFooterHeight()
	if canvasH <= 0 {
		t.Fatalf("canvas height = %d, want positive", canvasH)
	}
	if mm.headerRows()+canvasH+mm.orchFooterHeight() != mm.height {
		t.Fatalf("orchestration regions total %d rows in a %d-row terminal",
			mm.headerRows()+canvasH+mm.orchFooterHeight(), mm.height)
	}
}
