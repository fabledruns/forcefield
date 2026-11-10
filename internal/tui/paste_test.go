package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/config"
	"forcefield/internal/session"
)

// newTestModel builds a minimal model sized like a real terminal, without
// spinning up a Runtime (which needs a loaded config/provider). This is
// enough to exercise handleKey's input-editing paths, which never touch
// m.runtime unless Enter is pressed.
func newTestModel() model {
	m := model{
		input:        newInput(),
		width:        80,
		height:       24,
		registry:     newRegistry(),
		mouseEnabled: true,
	}
	m.layout()
	return m
}

// pasteMsg builds the tea.KeyMsg bubbletea's bracketed-paste reader emits
// for a clipboard paste: one message, Type KeyRunes, Paste set, carrying
// the full pasted text (including any newlines) as runes.
func pasteMsg(text string) tea.KeyMsg {
	return tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(text),
		Paste: true,
	}
}

func TestPasteWithNewlinesBecomesPlaceholderNotSubmitted(t *testing.T) {
	// Indentation uses spaces here (not a literal tab) because the
	// underlying textarea widget expands tabs to 4 spaces on input, same
	// as it would for a typed tab -- an intentional, pre-existing widget
	// behavior. Newlines are what the old bug was about; the paste now
	// collapses to one placeholder token and the original content keeps
	// its newlines and indentation verbatim in the store.
	pasted := "line one\n    line two indented\n\nline four after a blank line"

	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(pasted))
	got := next.(model)

	if len(got.entries) != 0 {
		t.Fatalf("paste was treated as a submission: got %d transcript entries, want 0", len(got.entries))
	}
	token := pastePlaceholder(4, 1)
	if got.input.Value() != token {
		t.Errorf("input.Value() = %q, want the compact placeholder %q", got.input.Value(), token)
	}
	if got.input.LineCount() != 1 {
		t.Errorf("input.LineCount() = %d, want 1 (the placeholder is a single line)", got.input.LineCount())
	}
	if content := got.pastes[token]; content != pasted {
		t.Errorf("paste store holds %q, want the original %q (newlines/indentation preserved verbatim)", content, pasted)
	}
}

func TestPasteWithCodeBlockPreservesStructure(t *testing.T) {
	pasted := "Please review:\n\n```go\nfunc main() {\n    fmt.Println(\"hi\")\n}\n```\n"

	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(pasted))
	got := next.(model)

	// The visible input is the compact token (the trailing newline
	// closes line 7 rather than opening an empty line 8)…
	token := pastePlaceholder(7, 1)
	if got.input.Value() != token {
		t.Errorf("input.Value() = %q, want placeholder %q", got.input.Value(), token)
	}
	// …and the stored original keeps the code block byte-for-byte,
	// ready to be submitted unmodified.
	if content := got.pastes[token]; content != pasted {
		t.Errorf("code block was mangled by paste handling:\nstored:  %q\nwant: %q", content, pasted)
	}
}

// newSubmitTestModel builds a full model via newModel, the same
// constructor production code uses, so the Enter/submit path (which goes
// through m.runtime and m.session) can be exercised too. It sandboxes
// HOME/USERPROFILE and the working directory so config/session files land
// in a throwaway temp dir instead of the real environment - on Windows,
// os.UserHomeDir prefers USERPROFILE, so both must be redirected or the
// test would read (and submit against) the developer's real provider
// configuration.
func newSubmitTestModel(t *testing.T) model {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	t.Chdir(t.TempDir())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() failed: %v", err)
	}
	sess := session.New()

	m, err := newModel(cfg, sess, &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() failed: %v", err)
	}
	m.width, m.height = 80, 24
	m.layout()
	return m
}

func TestPlainEnterStillSubmits(t *testing.T) {
	m := newSubmitTestModel(t)
	m.input.SetValue("hello there")

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(model)

	if got.input.Value() != "" {
		t.Errorf("input was not cleared after Enter submit, got %q", got.input.Value())
	}
	if len(got.entries) == 0 || got.entries[0].Content != "hello there" {
		t.Fatalf("Enter did not submit the typed message as a single entry: entries=%+v", got.entries)
	}
}

func TestPasteThenManualEnterSubmitsWholePastedPrompt(t *testing.T) {
	pasted := "first line\nsecond line\nthird line"

	m := newSubmitTestModel(t)
	next, _ := m.handleKey(pasteMsg(pasted))
	m = next.(model)

	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(model)

	if len(got.entries) == 0 || got.entries[0].Content != pasted {
		t.Fatalf("submitted entry = %+v, want a single entry with the full pasted text", got.entries)
	}
}

func TestLargePasteIsInsertedEfficientlyInOneUpdate(t *testing.T) {
	// A large paste must be handled as a single insert in one Update —
	// one placeholder token replacing the whole content — not by
	// iterating per character through repeated Update calls, and not by
	// pushing 500 lines into the textarea.
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("line of pasted content\n")
	}
	pasted := b.String()

	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(pasted))
	got := next.(model)

	token := pastePlaceholder(500, 1)
	if got.input.Value() != token {
		t.Errorf("large multiline paste did not collapse to its placeholder; input = %q", got.input.Value())
	}
	if got.input.LineCount() != 1 {
		t.Errorf("input.LineCount() = %d, want 1 (placeholder token)", got.input.LineCount())
	}
	if content := got.pastes[token]; content != pasted {
		t.Errorf("paste store lost the large content: %d bytes stored, want %d", len(content), len(pasted))
	}
}

// shiftEnterMsg builds the tea.KeyMsg that real terminals send for
// Shift+Enter: a bare line feed (0x0A), which Bubble Tea reports as
// KeyCtrlJ since it has no way to represent a Shift modifier on Enter.
// See the comment on input.KeyMap.InsertNewline in newInput.
func shiftEnterMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyCtrlJ}
}

func TestShiftEnterInsertsNewlineWithoutSubmitting(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("first line")
	m.input.CursorEnd()

	next, _ := m.handleKey(shiftEnterMsg())
	got := next.(model)

	if len(got.entries) != 0 {
		t.Fatalf("Shift+Enter submitted the prompt: got %d transcript entries, want 0", len(got.entries))
	}
	if got.input.Value() != "first line\n" {
		t.Errorf("input.Value() = %q, want %q (Shift+Enter should insert a newline)", got.input.Value(), "first line\n")
	}
	if got.input.LineCount() != 2 {
		t.Errorf("input.LineCount() = %d, want 2", got.input.LineCount())
	}
}

func TestShiftEnterGrowsInputHeightUpToMax(t *testing.T) {
	m := newTestModel()
	if got := m.input.Height(); got != minInputHeight {
		t.Fatalf("input.Height() = %d before typing, want %d", got, minInputHeight)
	}

	// Add more lines than maxInputHeight allows, via repeated Shift+Enter,
	// and confirm the box grows with each one but never past the cap.
	for i := 0; i < maxInputHeight+3; i++ {
		next, _ := m.handleKey(shiftEnterMsg())
		m = next.(model)

		want := i + 2 // started at 1 line, each press adds one
		if want > maxInputHeight {
			want = maxInputHeight
		}
		if got := m.input.Height(); got != want {
			t.Errorf("after %d Shift+Enter presses, input.Height() = %d, want %d", i+1, got, want)
		}
	}
}

func TestPlainEnterDoesNotTriggerShiftEnterBinding(t *testing.T) {
	// A plain Enter (KeyEnter, i.e. carriage return) must never be
	// mistaken for the Shift+Enter/Ctrl+J newline shortcut (KeyCtrlJ,
	// line feed): they are distinct tea.KeyType values.
	if tea.KeyEnter == tea.KeyCtrlJ {
		t.Fatal("tea.KeyEnter and tea.KeyCtrlJ must be distinct key types")
	}
}

func TestInputTextStyleIsBrightAndPlaceholderStaysMuted(t *testing.T) {
	input := newInput()

	textFg, ok := input.FocusedStyle.Text.GetForeground().(lipgloss.Color)
	if !ok {
		t.Fatalf("FocusedStyle.Text has no plain foreground color set: %#v", input.FocusedStyle.Text.GetForeground())
	}
	if textFg != colorText {
		t.Errorf("FocusedStyle.Text foreground = %v, want the same bright colorText (%v) used elsewhere in the transcript", textFg, colorText)
	}

	cursorLineFg, ok := input.FocusedStyle.CursorLine.GetForeground().(lipgloss.Color)
	if !ok {
		t.Fatalf("FocusedStyle.CursorLine has no plain foreground color set: %#v", input.FocusedStyle.CursorLine.GetForeground())
	}
	if cursorLineFg != colorText {
		t.Errorf("FocusedStyle.CursorLine foreground = %v, want colorText (%v); this is the style actually applied to the line being typed on", cursorLineFg, colorText)
	}

	placeholderFg, ok := input.FocusedStyle.Placeholder.GetForeground().(lipgloss.Color)
	if !ok {
		t.Fatalf("FocusedStyle.Placeholder has no plain foreground color set: %#v", input.FocusedStyle.Placeholder.GetForeground())
	}
	if placeholderFg != colorMuted {
		t.Errorf("FocusedStyle.Placeholder foreground = %v, want colorMuted (%v)", placeholderFg, colorMuted)
	}

	if placeholderFg == textFg {
		t.Errorf("placeholder and typed-text colors must differ so the placeholder reads as visibly dimmer, not brighter or equal")
	}
}
