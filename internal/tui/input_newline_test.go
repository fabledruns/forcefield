package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/session"
)

func typeMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func altEnterMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
}

// press runs one key event through the full handleKey path, returning the
// evolved model (handleKey has a value receiver, so the returned model
// must be kept).
func press(m model, msg tea.KeyMsg) model {
	next, _ := m.handleKey(msg)
	return next.(model)
}

func ctrlJMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyCtrlJ}
}

func enterMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyEnter}
}

// newInputModel returns a model whose input path is fully wired (session,
// registry) so submissions can be driven end to end without a runtime.
func newInputModel() model {
	m := sizedModel()
	m.session = session.New()
	m.registry = newRegistry()
	return m
}

func TestEnterSubmitsViaAcceptInput(t *testing.T) {
	m := newInputModel()
	m = press(m, typeMsg("hello world"))

	started, quit := m.acceptInput()
	if quit || !started {
		t.Fatalf("acceptInput() = %v, %v; want started, not quitting", started, quit)
	}
	if m.input.Value() != "" {
		t.Errorf("input not reset after submit: %q", m.input.Value())
	}
	if len(m.entries) != 1 || m.entries[0].Role != roleUser || m.entries[0].Content != "hello world" {
		t.Fatalf("entries = %+v, want one user entry with the task", m.entries)
	}
	if !m.waiting {
		t.Error("waiting not set after submit")
	}
	if got := m.session.Messages[len(m.session.Messages)-1].Content; got != "hello world" {
		t.Errorf("session message = %q, want %q", got, "hello world")
	}
}

func TestAltEnterInsertsNewlineNotSubmit(t *testing.T) {
	m := newInputModel()
	m = press(m, typeMsg("one"))
	m = press(m, altEnterMsg())

	if got := m.input.Value(); got != "one\n" {
		t.Errorf("input.Value() = %q, want %q", got, "one\n")
	}
	if len(m.entries) != 0 {
		t.Errorf("alt+enter submitted: %d entries", len(m.entries))
	}
	if m.waiting {
		t.Error("alt+enter started a stream")
	}
}

func TestCtrlJInsertsNewline(t *testing.T) {
	m := newInputModel()
	m = press(m, ctrlJMsg())
	if got := m.input.Value(); got != "\n" {
		t.Errorf("input.Value() = %q, want %q", got, "\n")
	}
}

func TestMultipleNewlineChords(t *testing.T) {
	m := newInputModel()
	m = press(m, typeMsg("a"))
	m = press(m, altEnterMsg())
	m = press(m, altEnterMsg())
	m = press(m, ctrlJMsg())

	if got := m.input.Value(); got != "a\n\n\n" {
		t.Errorf("input.Value() = %q, want %q", got, "a\n\n\n")
	}
	if m.input.LineCount() != 4 {
		t.Errorf("input.LineCount() = %d, want 4", m.input.LineCount())
	}
}

func TestMixedTypingAndNewlineChords(t *testing.T) {
	m := newInputModel()
	m = press(m, typeMsg("first"))
	m = press(m, altEnterMsg())
	m = press(m, typeMsg("second"))
	m = press(m, ctrlJMsg())
	m = press(m, typeMsg("third"))

	want := "first\nsecond\nthird"
	if got := m.input.Value(); got != want {
		t.Errorf("input.Value() = %q, want %q", got, want)
	}
	if len(m.entries) != 0 {
		t.Error("mixed typing/newlines unexpectedly submitted")
	}
}

func TestUnicodeAndNewlinesSurviveToSubmission(t *testing.T) {
	m := newInputModel()
	pasted := "héllo wörld\n日本語 ✓\n→ αβγ"
	m = press(m, pasteMsg(pasted))
	if got := m.input.Value(); got != pasted {
		t.Fatalf("input.Value() = %q, want verbatim paste %q", got, pasted)
	}

	started, _ := m.acceptInput()
	if !started {
		t.Fatal("acceptInput did not start")
	}

	stored := m.session.Messages[len(m.session.Messages)-1].Content
	if stored != pasted {
		t.Errorf("session message = %q, want verbatim", stored)
	}
	if strings.Contains(stored, "\\n") {
		t.Errorf("session message contains a literal backslash-n: %q", stored)
	}
	if !strings.Contains(stored, "\n") {
		t.Errorf("session message lost its real newline characters: %q", stored)
	}
	entry := m.entries[len(m.entries)-1]
	if entry.Role != roleUser || entry.Content != pasted {
		t.Errorf("transcript entry = %+v, want verbatim user message", entry)
	}
}

func TestMultilinePasteSubmittedWithRealNewlines(t *testing.T) {
	m := newInputModel()
	pasted := "line one\nline two\nline three"
	m = press(m, pasteMsg(pasted))

	started, _ := m.acceptInput()
	if !started {
		t.Fatal("acceptInput did not start")
	}
	if len(m.session.Messages) != 1 {
		t.Fatalf("session messages = %d, want 1", len(m.session.Messages))
	}
	stored := m.session.Messages[0].Content
	if stored != pasted {
		t.Errorf("submitted message = %q, want %q with real newlines", stored, pasted)
	}
}

func TestPasteCRLFNormalizedToLF(t *testing.T) {
	m := newInputModel()
	m = press(m, pasteMsg("windows\r\nstyle\r\npaste"))
	if got := m.input.Value(); got != "windows\nstyle\npaste" {
		t.Errorf("input.Value() = %q, want CRLF normalized to LF", got)
	}
}

func TestPasteBurstEnterInsertsNewlineNotSubmit(t *testing.T) {
	// Simulates the Windows console driver, where a paste has no bracketed
	// paste markers: characters arrive as rapid keystrokes and each pasted
	// newline is a plain Enter event.
	m := newInputModel()
	m = press(m, typeMsg("line one"))
	m = press(m, enterMsg()) // arrives immediately: burst window active
	m = press(m, typeMsg("line two"))

	if got := m.input.Value(); got != "line one\nline two" {
		t.Errorf("input.Value() = %q, want pasted newline preserved", got)
	}
	if len(m.entries) != 0 {
		t.Fatalf("burst Enter submitted mid-paste: %d entries", len(m.entries))
	}
}

func TestEnterAfterBurstWindowStillReachesSubmitPath(t *testing.T) {
	// A deliberate Enter more than pasteBurstWindow after the last
	// keystroke must not be mistaken for a pasted newline. (The submit leg
	// itself needs a live runtime, so it's covered by the integration
	// test's /help run; here we assert the branch decision only.)
	m := newInputModel()
	m = press(m, typeMsg("hello"))
	m.lastKeyAt = time.Now().Add(-time.Second) // simulate a long pause

	inBurst := time.Since(m.lastKeyAt) <= pasteBurstWindow
	if newlineEnter(enterMsg(), inBurst) {
		t.Error("delayed plain Enter was classified as a newline insert")
	}
	if !newlineEnter(enterMsg(), true) {
		t.Error("burst Enter was not classified as a newline insert")
	}
	if !newlineEnter(altEnterMsg(), false) {
		t.Error("alt+enter was not classified as a newline insert")
	}
}

func TestQuoteCommandCollapsesMultiline(t *testing.T) {
	got := quoteCommand("go test ./...\ngo vet ./...")
	if strings.Contains(got, "\\n") {
		t.Errorf("quoteCommand produced a literal backslash-n: %s", got)
	}
	if !strings.Contains(got, `go test ./...`) || !strings.Contains(got, "+1 more lines") {
		t.Errorf("quoteCommand = %q, want first line plus a hidden-line count", got)
	}
	if single := quoteCommand("echo hi"); single != `"echo hi"` {
		t.Errorf("quoteCommand(single line) = %q", single)
	}
}

func TestMultilineInputRendersAsSeparateLines(t *testing.T) {
	m := newInputModel()
	m = press(m, typeMsg("alpha"))
	m = press(m, altEnterMsg())
	m = press(m, typeMsg("beta"))

	view := stripANSI(m.input.View())
	alphaLine, betaLine := -1, -1
	for i, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "alpha") {
			alphaLine = i
		}
		if strings.Contains(line, "beta") {
			betaLine = i
		}
	}
	if alphaLine < 0 || betaLine < 0 {
		t.Fatalf("input view lost a line:\n%s", view)
	}
	if alphaLine == betaLine {
		t.Errorf("multiline input rendered on one line:\n%s", view)
	}
	if strings.Contains(view, "\\n") {
		t.Errorf("input view shows literal backslash-n:\n%s", view)
	}
}
