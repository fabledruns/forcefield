package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/permissions"
	"forcefield/internal/session"
)

// These tests pin hit-region geometry to the RENDERED transcript/footer/
// picker bytes instead of trusting the span bookkeeping. A previous
// version of the separator stride drifted one row per preceding entry,
// so clicks had to land below the visible text; only comparing regions
// against rendered output catches that class of bug.

func TestSpansMatchRenderedRows(t *testing.T) {
	m := visibleBlocksModel(t)

	content, spans := renderTranscriptWithLayout(m.entries, m.viewport.Width, "")
	lines := strings.Split(content, "\n")

	if len(spans) == 0 {
		t.Fatal("no interactive spans produced")
	}
	for _, s := range spans {
		if s.startLine >= len(lines) {
			t.Fatalf("span %+v starts past end of rendered content (%d lines)", s, len(lines))
		}
		row := stripANSI(lines[s.startLine])

		switch s.action {
		case actionToggleTool:
			want := m.entries[s.entry].Content
			if !strings.Contains(row, want) {
				t.Errorf("span for entry %d starts on %q, want a row containing %q", s.entry, row, want)
			}
		case actionToggleThinking:
			if !strings.Contains(row, "Thinking") {
				t.Errorf("span for entry %d starts on %q, want the Thinking header", s.entry, row)
			}
		}

		// Every block after the first must have exactly one blank row above
		// it (the entry separator), locking the stride arithmetic.
		if s.startLine > 0 && s.entry > 0 {
			if got := stripANSI(lines[s.startLine-1]); got != "" {
				t.Errorf("row above span start = %q, want the blank separator row", got)
			}
		}
	}
}

func TestSpanStartsAreStableAcrossLongTranscripts(t *testing.T) {
	m := longTranscriptModel(t)

	content := renderTranscript(m.entries, m.viewport.Width)
	lines := strings.Split(content, "\n")

	for _, s := range m.spans {
		row := stripANSI(lines[s.startLine])
		switch s.action {
		case actionToggleTool:
			want := m.entries[s.entry].Content
			if !strings.Contains(row, want) {
				t.Errorf("tool span (entry %d) drifted: startLine %d holds %q, want %q",
					s.entry, s.startLine, truncateForLog(row), want)
			}
		case actionToggleThinking:
			if !strings.Contains(row, "Thinking") {
				t.Errorf("thinking span (entry %d) drifted: startLine %d holds %q",
					s.entry, s.startLine, truncateForLog(row))
			}
		}
	}
}

// TestClickLandsOnVisibleText is the end-to-end guard for the reported
// symptom: clicking the exact row where a block is DRAWN must act on it.
func TestClickLandsOnVisibleText(t *testing.T) {
	m := visibleBlocksModel(t)

	for _, s := range m.spans {
		screenY := m.headerRows() + s.startLine - m.viewport.YOffset

		next, consumed := m.routeMouse(leftClick(3, screenY))
		m = next
		if !consumed {
			t.Fatalf("click on drawn block row %d was not consumed", screenY)
		}

		var expanded bool
		switch s.action {
		case actionToggleTool:
			expanded = m.entries[s.entry].Tool.expanded
		case actionToggleThinking:
			expanded = m.entries[s.entry].Thinking.expanded
		}
		if !expanded {
			t.Fatalf("clicking the drawn row (y=%d, span %s) did not expand the block", screenY, s.id)
		}

		// Toggle back so later spans start from a clean state.
		next, _ = m.routeMouse(leftClick(3, screenY))
		m = next
	}
}

func TestHoverHighlightsExactlyTheDrawnBlock(t *testing.T) {
	m := visibleBlocksModel(t)

	for _, s := range m.spans {
		screenY := m.headerRows() + s.startLine - m.viewport.YOffset
		next, _ := m.routeMouse(tea.MouseMsg{X: 2, Y: screenY, Action: tea.MouseActionMotion})
		got := next.hoverID
		if got != s.id {
			t.Errorf("hover at drawn row %d = %q, want %q", screenY, got, s.id)
		}
	}
}

// ---- footer ground truth -----------------------------------------------------

func TestPermissionOptionsMatchRenderedFooter(t *testing.T) {
	m := newTestModel()
	ch := make(chan permissions.Prompt, 1)
	m.permissionPrompt = &permissionPrompt{
		request: permissions.Request{Tool: "shell", Arguments: map[string]any{"command": "ls"}},
		respond: ch,
	}

	rects := m.permissionOptionRects()
	lines := strings.Split(stripANSI(m.renderFooter()), "\n")
	optionsRow := len(lines) - 1 - 2 // above: help line + box bottom border
	if optionsRow < 0 {
		t.Fatalf("footer too short to hold an options line:\n%s", m.renderFooter())
	}

	keys := []string{"(y)", "(n)", "(a)", "(d)"}
	labels := []string{"(y) yes", "(n) no", "(a) always allow", "(d) always deny"}
	for i, r := range rects {
		col := strings.Index(lines[optionsRow], labels[i])
		if col < 0 {
			t.Fatalf("label %q missing from rendered options row %q", labels[i], lines[optionsRow])
		}
		// Footer rows map one-to-one onto screen columns starting at x=0,
		// so the rendered index IS the expected rect X.
		if r.Rect.X != col || r.Rect.W != len(labels[i]) {
			t.Errorf("%s rect X/W = %d/%d, want %d/%d (rendered)", keys[i], r.Rect.X, r.Rect.W, col, len(labels[i]))
		}
	}

	// And clicking where the text actually renders answers the prompt.
	clicked := false
	for i, r := range rects {
		if keys[i] != "(n)" {
			continue
		}
		next, _ := m.routeMouse(leftClick(r.Rect.X+r.Rect.W/2, r.Rect.Y))
		m = next
		select {
		case answer := <-ch:
			if answer != permissions.PromptDenyOnce {
				t.Errorf("clicking rendered (n) answered %v", answer)
			}
			clicked = true
		default:
			t.Fatal("clicking the rendered (n) label produced no answer")
		}
	}
	if !clicked {
		t.Fatal("(n) was never exercised")
	}
}

// ---- picker ground truth ------------------------------------------------------

func TestSessionPickerRowMatchesRenderedView(t *testing.T) {
	m := newTestModel()
	ids := []string{"id-aaa", "id-bbb", "id-ccc"}
	sessions := make([]session.Session, len(ids))
	for i, id := range ids {
		sessions[i] = session.Session{ID: id}
	}
	m.picker = newSessionPicker(sessions, "id-bbb")

	p := m.picker
	bx, by := p.boxOrigin(m.width, m.height)
	viewLines := strings.Split(stripANSI(p.view(m.width, m.height)), "\n")

	for wantRow, id := range ids {
		lineIdx := -1
		for i, l := range viewLines {
			if strings.Contains(l, "("+id) { // short ID shown in the row (8 chars max)
				lineIdx = i
				break
			}
		}
		if lineIdx < 0 {
			t.Fatalf("row for %s not found in rendered picker", id)
		}

		// Vertical: rendered line must equal origin + rows-top offset + index.
		if got := lineIdx - by; got != pickerRowsTop+wantRow {
			t.Errorf("row %s renders %d rows below box top, want %d", id, got, pickerRowsTop+wantRow)
		}

		// Horizontal: some column inside the row must satisfy rowAt.
		hit := false
		for x := bx; x < bx+pickerWidth+4; x++ {
			if idx, ok := p.rowAt(x, lineIdx, m.width, m.height); ok && idx == wantRow {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("rowAt never resolves the rendered row for %s", id)
		}
	}

	// A row clearly outside the modal must not resolve.
	if _, ok := p.rowAt(1, 1, m.width, m.height); ok {
		t.Error("top-left corner resolved as a picker row")
	}
}

// ---- helpers -------------------------------------------------------------------

// stripANSI is defined in markdown_reliability_test.go; the ground-truth
// tests reuse it so styled output can be substring-checked.

func truncateForLog(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:57] + "..."
}
