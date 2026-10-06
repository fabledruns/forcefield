package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// keyPress resets the paste-burst clock so a synthetic key is read as a
// deliberate press, never as a Windows-console paste burst.
func keyPress(m model, typ tea.KeyType) (model, tea.Cmd) {
	m.lastKeyAt = time.Time{}
	next, cmd := m.handleKey(tea.KeyMsg{Type: typ})
	return next.(model), cmd
}

// paletteLines renders the palette and splits it into rows.
func paletteLines(t *testing.T, m model) []string {
	t.Helper()
	out := m.renderSuggestions()
	if out == "" {
		return nil
	}
	return strings.Split(stripANSI(out), "\n")
}

func openPalette(t *testing.T, value string) model {
	t.Helper()
	m := newTestModel()
	m.input.SetValue(value)
	m.updateSuggestions()
	m.layout()
	return m
}

func TestPaletteOpensOnSlash(t *testing.T) {
	m := openPalette(t, "/")
	if !m.paletteOpen() {
		t.Fatal("palette did not open on bare \"/\"")
	}
	rows := paletteLines(t, m)
	if len(rows) != m.paletteShown() {
		t.Fatalf("rendered %d rows, want %d (one row per command)", len(rows), m.paletteShown())
	}
	if len(rows) > maxSuggestions {
		t.Fatalf("rendered %d rows, want at most %d", len(rows), maxSuggestions)
	}
	for i, row := range rows {
		name := "/" + m.suggestions[i].Name()
		if !strings.Contains(row, name) {
			t.Errorf("row %d = %q, want command %q", i, row, name)
		}
		if !strings.Contains(row, m.suggestions[i].Description()) {
			t.Errorf("row %d = %q, want one-line description", i, row)
		}
	}
}

func TestPaletteFiltersAsYouType(t *testing.T) {
	m := openPalette(t, "/mo")
	rows := paletteLines(t, m)
	if len(rows) == 0 {
		t.Fatal("palette closed on \"/mo\", want filtered matches")
	}
	for i, row := range rows {
		if !strings.Contains(row, "/"+m.suggestions[i].Name()) {
			t.Errorf("row %d = %q, want filtered command", i, row)
		}
		if !strings.HasPrefix(m.suggestions[i].Name(), "mo") {
			t.Errorf("row %d matches %q, want prefix \"mo\"", i, m.suggestions[i].Name())
		}
	}
}

func TestPaletteHidesOnExactMatch(t *testing.T) {
	m := openPalette(t, "/help")
	if m.paletteOpen() {
		t.Error("palette stayed open on exact command name, want it hidden")
	}
}

func TestPaletteArrowNavWraps(t *testing.T) {
	m := openPalette(t, "/")
	shown := m.paletteShown()
	if shown < 2 {
		t.Skip("need at least 2 rows to test navigation")
	}

	m, _ = keyPress(m, tea.KeyDown)
	if m.suggestionCursor != 1 {
		t.Fatalf("Down moved cursor to %d, want 1", m.suggestionCursor)
	}

	// Walk to the end, then once more: it wraps to the top.
	for i := 0; i < shown-1; i++ {
		m, _ = keyPress(m, tea.KeyDown)
	}
	if m.suggestionCursor != 0 {
		t.Fatalf("Down past the end = %d, want wraparound to 0", m.suggestionCursor)
	}

	m, _ = keyPress(m, tea.KeyUp)
	if m.suggestionCursor != shown-1 {
		t.Fatalf("Up from top = %d, want wraparound to %d", m.suggestionCursor, shown-1)
	}
}

func TestPaletteEnterSelectsActive(t *testing.T) {
	m := openPalette(t, "/")
	want := "/" + m.suggestions[1].Name()

	m, _ = keyPress(m, tea.KeyDown)
	m, _ = keyPress(m, tea.KeyEnter)

	if m.input.Value() != want {
		t.Fatalf("Enter completed %q, want %q", m.input.Value(), want)
	}
	if m.paletteOpen() {
		t.Error("palette stayed open after selecting an exact command")
	}
	if len(m.entries) != 0 {
		t.Errorf("Enter ran the command (%d entries), want completion only", len(m.entries))
	}
}

func TestPaletteSecondEnterRuns(t *testing.T) {
	m := openPalette(t, "/he")
	m, _ = keyPress(m, tea.KeyEnter)
	if m.input.Value() != "/help" {
		t.Fatalf("first Enter = %q, want %q", m.input.Value(), "/help")
	}
	// Second Enter with the palette closed dispatches as before.
	m, _ = keyPress(m, tea.KeyEnter)
	if len(m.entries) == 0 {
		t.Error("second Enter did not run the selected command")
	}
}

func TestPaletteTabSelectsActive(t *testing.T) {
	m := openPalette(t, "/")
	want := "/" + m.suggestions[2].Name()
	m.suggestionCursor = 2

	m, _ = keyPress(m, tea.KeyTab)
	if m.input.Value() != want {
		t.Fatalf("Tab completed %q, want %q", m.input.Value(), want)
	}
}

func TestPaletteActiveRowUsesPink(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := openPalette(t, "/")
	m.suggestionCursor = 1
	rows := strings.Split(m.renderSuggestions(), "\n")
	const pink = "237;38;99" // #ED2663 in true color
	if !strings.Contains(rows[1], pink) {
		t.Errorf("active row missing Forcefield pink:\n%s", rows[1])
	}
	for i, row := range rows {
		if i != 1 && strings.Contains(row, pink) {
			t.Errorf("inactive row %d uses active pink:\n%s", i, row)
		}
	}
}

func TestPaletteHasNoBorders(t *testing.T) {
	m := openPalette(t, "/")
	for i, row := range paletteLines(t, m) {
		for _, bad := range []string{"│", "─", "╭", "╮", "╰", "╯", "┃", "━"} {
			if strings.Contains(row, bad) {
				t.Errorf("row %d has border %q: %q", i, bad, row)
			}
		}
		if w := lipgloss.Width(row); w > m.width {
			t.Errorf("row %d wraps (%d > %d): %q", i, w, m.width, row)
		}
	}
}

func TestPaletteClickSelectsRow(t *testing.T) {
	m := openPalette(t, "/")
	top := m.height - m.footerHeight()
	want := "/" + m.suggestions[2].Name()

	next, consumed := m.routeMouse(leftClick(4, top+2))
	m = next
	if !consumed {
		t.Fatal("palette click was not consumed")
	}
	if m.input.Value() != want {
		t.Fatalf("click completed %q, want %q", m.input.Value(), want)
	}
}

func TestPaletteFooterHeightMatchesRows(t *testing.T) {
	m := openPalette(t, "/")
	base := 2 + m.input.Height() + 1 // border + input + status line
	if got := m.footerHeight(); got != base+m.paletteShown() {
		t.Errorf("footerHeight = %d, want %d + %d palette rows", got, base, m.paletteShown())
	}
	m.input.SetValue("/zzz-no-such-command")
	m.updateSuggestions()
	m.layout()
	if m.paletteOpen() {
		t.Fatal("palette open with no matches")
	}
	if got := m.footerHeight(); got != base {
		t.Errorf("footerHeight with no matches = %d, want %d", got, base)
	}
}
