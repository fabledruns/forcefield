package tui

// Paste placeholders: a bracketed paste collapses to a compact token
// (`[pasted text 12 lines #3]`) backed by a store of the original
// content, which is reconstructed verbatim at submission. These tests
// pin the three contracts that matter — paste detection (a paste is
// not typing), placeholder rendering (the content never floods the
// input), and exact reconstruction (submission is byte-for-byte what
// was pasted) — plus the editing, multi-paste, and store-lifetime
// behaviors around them.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// typedRunesMsg builds the tea.KeyMsg normal typing produces: KeyRunes
// with Paste unset. bubbles inserts the full rune slice.
func typedRunesMsg(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)}
}

// lines joins n copies of a labeled line, each with a trailing newline,
// so pasteLineCount's trailing-newline rule is exercised.
func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line ")
		b.WriteString(itoaForTest(i))
		b.WriteByte('\n')
	}
	return b.String()
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestPasteLineCountTrailingNewlineClosesLine pins the line-counting
// rule the placeholder reports: a trailing newline closes the last
// line rather than opening an empty one.
func TestPasteLineCountTrailingNewlineClosesLine(t *testing.T) {
	for _, tc := range []struct {
		content string
		want    int
	}{
		{"", 1},         // degenerate: an empty paste counts as one line
		{"a", 1},        // single line, unterminated
		{"a\n", 1},      // single line, trailing newline closes it
		{"a\nb", 2},     // two lines, unterminated
		{"a\nb\n", 2},   // two lines, trailing newline closes the second
		{"a\n\nb\n", 3}, // blank line in the middle counts
		{"a\nb\n\n", 3}, // one trailing blank, closed by the suffix rule
		{"\n", 1},       // a lone newline is one (empty) line
	} {
		if got := pasteLineCount(tc.content); got != tc.want {
			t.Errorf("pasteLineCount(%q) = %d, want %d", tc.content, got, tc.want)
		}
	}
}

// TestPastePlaceholderFormat pins the exact token format, including
// the singular form and the unique id.
func TestPastePlaceholderFormat(t *testing.T) {
	for _, tc := range []struct {
		n, id int
		want  string
	}{
		{42, 1, "[pasted text 42 lines #1]"},
		{1, 3, "[pasted text 1 line #3]"},
		{2, 7, "[pasted text 2 lines #7]"},
	} {
		if got := pastePlaceholder(tc.n, tc.id); got != tc.want {
			t.Errorf("pastePlaceholder(%d, %d) = %q, want %q", tc.n, tc.id, got, tc.want)
		}
	}
}

// TestTypedRunesAreNeverPlaceholders proves the detection boundary:
// typing is not a paste. The exact token text typed by hand stays
// literal in the input and puts nothing in the store — expansion later
// must leave it untouched, since no paste backs it.
func TestTypedRunesAreNeverPlaceholders(t *testing.T) {
	m := newTestModel()
	typed := "[pasted text 4 lines #1]"

	next, _ := m.handleKey(typedRunesMsg(typed))
	got := next.(model)

	if got.input.Value() != typed {
		t.Errorf("typed text was altered: got %q, want %q", got.input.Value(), typed)
	}
	if len(got.pastes) != 0 {
		t.Errorf("typing created %d store entries, want 0: %v", len(got.pastes), got.pastes)
	}
	if got.input.LineCount() != 1 {
		t.Errorf("input.LineCount() = %d, want 1", got.input.LineCount())
	}
}

// TestTypedRunesAfterPasteStayLiteral pins that a paste followed by
// ordinary typing appends the typed characters verbatim after the
// token — typing must keep working exactly as before.
func TestTypedRunesAfterPasteStayLiteral(t *testing.T) {
	m := newTestModel()
	pasted := lines(3)

	next, _ := m.handleKey(pasteMsg(pasted))
	m = next.(model)
	next, _ = m.handleKey(typedRunesMsg(" tail"))
	m = next.(model)

	want := pastePlaceholder(3, 1) + " tail"
	if m.input.Value() != want {
		t.Errorf("input.Value() = %q, want %q", m.input.Value(), want)
	}
}

// TestPlaceholderRendersInView proves the compact token is what the
// user sees: the rendered input contains the placeholder and none of
// the pasted content.
func TestPlaceholderRendersInView(t *testing.T) {
	pasted := "SECRET-CONTENT-" + strings.Repeat("x", 200)

	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(pasted))
	m = next.(model)

	view := m.input.View()
	token := pastePlaceholder(1, 1) // 300 chars, single line
	if !strings.Contains(view, token) {
		t.Errorf("rendered input does not show the placeholder %q; view = %q", token, view)
	}
	if strings.Contains(view, pasted) {
		t.Errorf("rendered input shows the raw pasted content instead of the placeholder")
	}
}

// TestPlaceholderExactFortyTwoLines checks the display format named in
// the requirement, with the canonical example.
func TestPlaceholderExactFortyTwoLines(t *testing.T) {
	m := newTestModel()

	next, _ := m.handleKey(pasteMsg(lines(42)))
	m = next.(model)

	if want := "[pasted text 42 lines #1]"; m.input.Value() != want {
		t.Errorf("input.Value() = %q, want %q", m.input.Value(), want)
	}
}

// TestSingleLinePasteThresholdBoundary pins the inline/placeholder
// boundary: a short single-line paste stays verbatim (natural to read
// and hand-edit); one char over the threshold collapses to a token.
func TestSingleLinePasteThresholdBoundary(t *testing.T) {
	inline := strings.Repeat("a", pastePlaceholderMinChars-1)
	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(inline))
	m = next.(model)
	if m.input.Value() != inline {
		t.Errorf("%d-char single-line paste became %q, want verbatim inline text", len(inline), m.input.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("inline paste created %d store entries, want 0", len(m.pastes))
	}

	token := strings.Repeat("b", pastePlaceholderMinChars)
	m2 := newTestModel()
	next, _ = m2.handleKey(pasteMsg(token))
	m2 = next.(model)
	if want := pastePlaceholder(1, 1); m2.input.Value() != want {
		t.Errorf("%d-char single-line paste shows %q, want placeholder %q", len(token), m2.input.Value(), want)
	}
	if content := m2.pastes[pastePlaceholder(1, 1)]; content != token {
		t.Errorf("paste store holds %q, want the original %d-char line", content, len(token))
	}
}

// TestMultiPasteGetsDistinctTokens proves several pastes in one
// message work: identical content still gets distinct ids, both
// tokens render in order, and reconstruction returns both originals.
func TestMultiPasteGetsDistinctTokens(t *testing.T) {
	pasted := "alpha\nbeta\n"

	m := newTestModel()
	next, _ := m.handleKey(typedRunesMsg("head "))
	m = next.(model)
	next, _ = m.handleKey(pasteMsg(pasted))
	m = next.(model)
	next, _ = m.handleKey(typedRunesMsg(" mid "))
	m = next.(model)
	next, _ = m.handleKey(pasteMsg(pasted))
	m = next.(model)

	tok1, tok2 := pastePlaceholder(2, 1), pastePlaceholder(2, 2)
	if tok1 == tok2 {
		t.Fatal("two pastes in one message produced identical tokens")
	}
	want := "head " + tok1 + " mid " + tok2
	if m.input.Value() != want {
		t.Errorf("input.Value() = %q, want %q", m.input.Value(), want)
	}
	if content := m.pastes[tok1]; content != pasted {
		t.Errorf("store[%q] = %q, want the original", tok1, pasted)
	}
	if content := m.pastes[tok2]; content != pasted {
		t.Errorf("store[%q] = %q, want the original", tok2, pasted)
	}

	if got := expandPastes(m.input.Value(), m.pastes); got != "head "+pasted+" mid "+pasted {
		t.Errorf("expandPastes() = %q, want both originals reconstructed in order", got)
	}
}

// TestSubmitReconstructsOriginalContent is the submission contract:
// the user pastes a multi-line prompt with typed text around it and
// presses Enter — the message Forcefield records and streams is the
// original content byte-for-byte, never the placeholder.
func TestSubmitReconstructsOriginalContent(t *testing.T) {
	pasted := "fix this:\n```go\nmain()\n```\nthanks"

	m := newSubmitTestModel(t)
	next, _ := m.handleKey(typedRunesMsg("Please "))
	m = next.(model)
	next, _ = m.handleKey(pasteMsg(pasted))
	m = next.(model)
	next, _ = m.handleKey(typedRunesMsg(" end"))
	m = next.(model)
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(model)

	want := "Please " + pasted + " end"
	if len(got.entries) == 0 || got.entries[0].Content != want {
		t.Fatalf("submitted entry = %+v, want the original pasted content with typed text around it: %q", got.entries, want)
	}
	if !strings.Contains(got.entries[0].Content, "```go") {
		t.Errorf("submitted content lost the code block: %q", got.entries[0].Content)
	}
	if strings.Contains(got.entries[0].Content, "[pasted text") {
		t.Errorf("submitted content still contains a placeholder token: %q", got.entries[0].Content)
	}
	if got.input.Value() != "" {
		t.Errorf("input was not cleared after submit: %q", got.input.Value())
	}
	if len(got.pastes) != 0 {
		t.Errorf("paste store was not cleared after submit: %v", got.pastes)
	}
}

// TestEditingAroundTokenKeepsReconstructionExact proves the token
// survives ordinary editing: append, backspace it away, append again
// — every intermediate state expands to exactly the original.
func TestEditingAroundTokenKeepsReconstructionExact(t *testing.T) {
	pasted := "one\ntwo\nthree"

	m := newSubmitTestModel(t)
	next, _ := m.handleKey(pasteMsg(pasted))
	m = next.(model)
	token := pastePlaceholder(3, 1)
	original := expandPastes(m.input.Value(), m.pastes)
	if original != pasted {
		t.Fatalf("paste alone expanded to %q, want %q", original, pasted)
	}

	// Type, then delete it rune by rune with backspace — the token
	// must stay intact (its deletion is rune-by-rune, so a partial
	// deletion leaves a literal fragment; full deletion restores the
	// exact token) and its backing content untouched throughout.
	next, _ = m.handleKey(typedRunesMsg(" xyz"))
	m = next.(model)
	for i := 0; i < 4; i++ {
		next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(model)
		if !strings.HasPrefix(m.input.Value(), token) {
			t.Fatalf("after %d backspaces input = %q, token %q damaged", i+1, m.input.Value(), token)
		}
		if m.input.Value() != token {
			t.Logf("after %d backspaces the tail is not fully deleted yet: %q", i+1, m.input.Value())
		}
	}
	if m.input.Value() != token {
		t.Fatalf("after all backspaces input = %q, want the intact token %q", m.input.Value(), token)
	}
	if got := expandPastes(m.input.Value(), m.pastes); got != pasted {
		t.Errorf("expansion after editing = %q, want %q", got, pasted)
	}

	// Submit what remains: the original content, nothing else.
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(model)
	if len(got.entries) == 0 || got.entries[0].Content != pasted {
		t.Fatalf("submitted entry = %+v, want the original %q", got.entries, pasted)
	}
}

// TestEscClearsInputAndPasteStore pins the store's lifetime on the
// Esc path: clearing the input discards the backing content too, so
// nothing outlives the message it belonged to.
func TestEscClearsInputAndPasteStore(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(lines(5)))
	m = next.(model)
	if len(m.pastes) == 0 {
		t.Fatal("precondition: paste stored nothing")
	}

	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)

	if m.input.Value() != "" {
		t.Errorf("input not cleared by Esc: %q", m.input.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("paste store not cleared by Esc: %v", m.pastes)
	}
}

// TestResetInputDropsStore pins the shared reset helper: input and
// store clear together.
func TestResetInputDropsStore(t *testing.T) {
	m := newTestModel()
	m.insertPaste(lines(2))
	if len(m.pastes) == 0 {
		t.Fatal("precondition: paste stored nothing")
	}
	m.resetInput()
	if m.input.Value() != "" || len(m.pastes) != 0 {
		t.Errorf("resetInput left input=%q store=%v", m.input.Value(), m.pastes)
	}
}

// TestPrunePastesKeepsLiveTokensOnly pins the pruning rule: entries
// whose token is still in the input survive; anything else is dead.
func TestPrunePastesKeepsLiveTokensOnly(t *testing.T) {
	m := newTestModel()
	live := pastePlaceholder(2, 1)
	m.insertPaste("a\nb\n")
	if m.input.Value() != live {
		t.Fatalf("precondition: input = %q, want %q", m.input.Value(), live)
	}
	m.pastes[pastePlaceholder(9, 9)] = "stale content"

	m.prunePastes()

	if _, ok := m.pastes[live]; !ok {
		t.Errorf("prunePastes dropped the live token's entry: %v", m.pastes)
	}
	if _, ok := m.pastes[pastePlaceholder(9, 9)]; ok {
		t.Errorf("prunePastes kept a stale entry whose token is not in the input: %v", m.pastes)
	}
	if len(m.pastes) != 1 {
		t.Errorf("prunePastes left %d entries, want 1", len(m.pastes))
	}
}

// TestExpandPastesPassesUnknownTextThrough covers the reconstruction
// scan's fail-safe cases: unknown tokens, unterminated fragments, and
// token-shaped text inside a stored paste are all data, never
// expanded.
func TestExpandPastesPassesUnknownTextThrough(t *testing.T) {
	unknown := "[pasted text 2 lines #42]"
	fragment := "[pasted text 2 lines no bracket"
	nested := "[pasted text 99 lines #7]"

	pastes := map[string]string{pastePlaceholder(1, 1): "real\ncontent\n"}

	for _, tc := range []struct{ value, want string }{
		{unknown, unknown},             // no such paste id
		{fragment, fragment},           // unterminated scan prefix
		{"plain words", "plain words"}, // nothing token-shaped
		{"", ""},                       // empty input
		{pastePlaceholder(1, 1) + nested, "real\ncontent\n" + nested}, // stored content is never re-scanned
	} {
		if got := expandPastes(tc.value, pastes); got != tc.want {
			t.Errorf("expandPastes(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
	if got := expandPastes("anything", nil); got != "anything" {
		t.Errorf("expandPastes with an empty store = %q, want the input unchanged", got)
	}
}

// TestEmptyPasteIsHarmless pins the degenerate paste: nothing shows,
// nothing is stored.
func TestEmptyPasteIsHarmless(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(""))
	m = next.(model)
	if m.input.Value() != "" {
		t.Errorf("empty paste inserted %q", m.input.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("empty paste stored %v", m.pastes)
	}
}

// TestShortSingleLinePasteStaysInline documents the small-paste path:
// a short single-line paste joins the input verbatim with no token.
func TestShortSingleLinePasteStaysInline(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleKey(pasteMsg("config.yaml"))
	m = next.(model)
	if m.input.Value() != "config.yaml" {
		t.Errorf("short paste became %q, want verbatim inline text", m.input.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("short paste stored %v", m.pastes)
	}
}

// TestSinglePasteProducesExactlyOnePlaceholder is the regression guard
// for the fragmented-placeholder regression: one clipboard paste must
// collapse into exactly one placeholder and one store entry, however
// large it is.
func TestSinglePasteProducesExactlyOnePlaceholder(t *testing.T) {
	pasted := strings.Repeat("pasted line of text\n", 500)

	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(pasted))
	got := next.(model)

	if n := strings.Count(got.input.Value(), pasteTokenPrefix); n != 1 {
		t.Fatalf("one paste produced %d placeholders: %q", n, got.input.Value())
	}
	if len(got.pastes) != 1 {
		t.Fatalf("one paste produced %d store entries: %v", len(got.pastes), got.pastes)
	}
	for token, content := range got.pastes {
		if content != pasted {
			t.Errorf("store[%q] holds %d bytes, want the original %d", token, len(content), len(pasted))
		}
	}
}

// TestUnmarkedConsolePasteNeverCreatesPlaceholders is the other half of
// the regression guard. The Windows console driver delivers a clipboard
// paste as one key event per character and never sets Paste: true, so
// nothing in the event stream marks it as a paste. Guessing from
// timing fragmented one paste into many placeholders (and corrupted
// normal typing), so the run of keys must fall back to the previous
// behavior: inserted as typed, with no placeholder invented.
func TestUnmarkedConsolePasteNeverCreatesPlaceholders(t *testing.T) {
	pasted := "first line\nsecond line\nthird line"

	m := newTestModel()
	for _, c := range pasted {
		var msg tea.KeyMsg
		if c == '\n' {
			msg = tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{c}}
		}
		next, _ := m.handleKey(msg)
		m = next.(model)
	}

	if n := strings.Count(m.input.Value(), pasteTokenPrefix); n != 0 {
		t.Fatalf("manually typed multiline text produced %d placeholders: %q", n, m.input.Value())
	}
	if len(m.pastes) != 0 {
		t.Fatalf("manually typed multiline text created store entries: %v", m.pastes)
	}
	if !strings.Contains(m.input.Value(), "second line") {
		t.Errorf("input.Value() = %q, want the text inserted verbatim", m.input.Value())
	}
}

// TestUnmarkedConsolePasteSubmitsExactContent keeps the submitted
// content exact on the manual multiline path: whatever the input holds is what
// goes out, with no placeholder left in the message.
func TestUnmarkedConsolePasteSubmitsExactContent(t *testing.T) {
	pasted := "alpha\nbeta\ngamma"

	m := newSubmitTestModel(t)
	for _, c := range pasted {
		var msg tea.KeyMsg
		if c == '\n' {
			msg = tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{c}}
		}
		next, _ := m.handleKey(msg)
		m = next.(model)
	}
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(model)

	if len(got.entries) == 0 || got.entries[0].Content != pasted {
		t.Fatalf("submitted entry = %+v, want the content %q", got.entries, pasted)
	}
}

// TestAtomicPlaceholderBackspace proves that pressing Backspace immediately after
// a paste placeholder deletes the entire placeholder in one operation and invalidates
// the stored payload.
func TestAtomicPlaceholderBackspace(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(lines(5)))
	m = next.(model)

	wantPlaceholder := "[pasted text 5 lines #1]"
	if m.input.Value() != wantPlaceholder {
		t.Fatalf("expected placeholder %q, got %q", wantPlaceholder, m.input.Value())
	}
	if len(m.pastes) != 1 {
		t.Fatalf("expected 1 paste in store, got %d", len(m.pastes))
	}

	// Press Backspace once
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)

	if m.input.Value() != "" {
		t.Errorf("expected empty input after backspacing placeholder, got %q", m.input.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("expected paste store to be empty after backspacing placeholder, got %v", m.pastes)
	}
}

// TestAtomicPlaceholderDelete proves that pressing Delete immediately before
// a paste placeholder deletes the entire placeholder forward and invalidates
// the stored payload.
func TestAtomicPlaceholderDelete(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleKey(pasteMsg(lines(5)))
	m = next.(model)

	wantPlaceholder := "[pasted text 5 lines #1]"
	if m.input.Value() != wantPlaceholder {
		t.Fatalf("expected placeholder %q, got %q", wantPlaceholder, m.input.Value())
	}
	if len(m.pastes) != 1 {
		t.Fatalf("expected 1 paste in store, got %d", len(m.pastes))
	}

	// Move cursor to start of line before '['
	m.input.CursorStart()

	// Press Delete once
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyDelete})
	m = next.(model)

	if m.input.Value() != "" {
		t.Errorf("expected empty input after deleting placeholder forward, got %q", m.input.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("expected paste store to be empty after deleting placeholder forward, got %v", m.pastes)
	}
}

// TestAtomicPlaceholderIndependentDeletion proves that multiple placeholders
// can be deleted independently without affecting other stored payloads or text.
func TestAtomicPlaceholderIndependentDeletion(t *testing.T) {
	m := newTestModel()
	p1 := lines(3)
	p2 := lines(8)

	// Paste 1
	next, _ := m.handleKey(pasteMsg(p1))
	m = next.(model)

	// Type separator
	next, _ = m.handleKey(typedRunesMsg(" and "))
	m = next.(model)

	// Paste 2
	next, _ = m.handleKey(pasteMsg(p2))
	m = next.(model)

	tok1 := "[pasted text 3 lines #1]"
	tok2 := "[pasted text 8 lines #2]"
	wantBefore := tok1 + " and " + tok2
	if m.input.Value() != wantBefore {
		t.Fatalf("expected input %q, got %q", wantBefore, m.input.Value())
	}
	if len(m.pastes) != 2 {
		t.Fatalf("expected 2 pastes in store, got %d", len(m.pastes))
	}

	// Backspace deletes tok2 atomically
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)

	wantAfter := tok1 + " and "
	if m.input.Value() != wantAfter {
		t.Errorf("expected input %q, got %q", wantAfter, m.input.Value())
	}
	if len(m.pastes) != 1 {
		t.Fatalf("expected 1 paste in store, got %d", len(m.pastes))
	}
	if content, ok := m.pastes[tok1]; !ok || content != p1 {
		t.Errorf("tok1 content corrupted or missing: %q", content)
	}
	if _, ok := m.pastes[tok2]; ok {
		t.Errorf("tok2 was not deleted from paste store")
	}

	// Now delete " and " using ordinary backspaces (5 chars: space, 'd', 'n', 'a', space)
	for i := 0; i < 5; i++ {
		next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(model)
	}

	if m.input.Value() != tok1 {
		t.Fatalf("expected input %q after removing separator, got %q", tok1, m.input.Value())
	}
	if len(m.pastes) != 1 {
		t.Fatalf("expected 1 paste in store, got %d", len(m.pastes))
	}

	// Backspace deletes tok1 atomically
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)

	if m.input.Value() != "" {
		t.Errorf("expected empty input after deleting tok1, got %q", m.input.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("expected empty paste store after deleting both placeholders, got %v", m.pastes)
	}
}

// TestAtomicPlaceholderSurroundingTextIntact proves that text before and after
// a placeholder remains intact when the placeholder is deleted.
func TestAtomicPlaceholderSurroundingTextIntact(t *testing.T) {
	m := newTestModel()
	pasted := lines(4)

	next, _ := m.handleKey(typedRunesMsg("prefix "))
	m = next.(model)
	next, _ = m.handleKey(pasteMsg(pasted))
	m = next.(model)
	next, _ = m.handleKey(typedRunesMsg(" suffix"))
	m = next.(model)

	tok := "[pasted text 4 lines #1]"
	wantInitial := "prefix " + tok + " suffix"
	if m.input.Value() != wantInitial {
		t.Fatalf("expected %q, got %q", wantInitial, m.input.Value())
	}

	// Move cursor backward past " suffix" (7 characters: 'x','i','f','f','u','s',' ')
	for i := 0; i < 7; i++ {
		next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
		m = next.(model)
	}

	// Cursor is now immediately after ']' of tok. Press Backspace:
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)

	wantFinal := "prefix  suffix"
	if m.input.Value() != wantFinal {
		t.Errorf("expected input %q, got %q", wantFinal, m.input.Value())
	}
	if len(m.pastes) != 0 {
		t.Errorf("expected paste store to be empty, got %v", m.pastes)
	}
}

// TestOrdinaryTextBackspaceNormal proves that backspace on ordinary text
// retains normal 1-character deletion behavior and is never made atomic.
func TestOrdinaryTextBackspaceNormal(t *testing.T) {
	m := newTestModel()
	next, _ := m.handleKey(typedRunesMsg("hello"))
	m = next.(model)

	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)

	if m.input.Value() != "hell" {
		t.Errorf("expected 'hell' after 1 backspace, got %q", m.input.Value())
	}

	// Delete forward at end does nothing
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyDelete})
	m = next.(model)
	if m.input.Value() != "hell" {
		t.Errorf("expected 'hell', got %q", m.input.Value())
	}

	// Move to start, delete forward deletes 1 char ('h')
	m.input.CursorStart()
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyDelete})
	m = next.(model)
	if m.input.Value() != "ell" {
		t.Errorf("expected 'ell' after 1 delete forward, got %q", m.input.Value())
	}
}

// TestPlaceholderEditedBecomesOrdinaryText proves that if the user edits
// the inside of a placeholder, only 1 character is modified, the store entry
// is safely invalidated, and subsequent editing remains normal.
func TestPlaceholderEditedBecomesOrdinaryText(t *testing.T) {
	m := newSubmitTestModel(t)
	pasted := lines(5)
	next, _ := m.handleKey(pasteMsg(pasted))
	m = next.(model)

	tok := "[pasted text 5 lines #1]"
	if m.input.Value() != tok {
		t.Fatalf("expected %q, got %q", tok, m.input.Value())
	}

	// Move cursor inside the placeholder (e.g., left 3 times)
	// Cursor is now between ' ' and '#1]'
	for i := 0; i < 3; i++ {
		next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyLeft})
		m = next.(model)
	}

	// Press Backspace: should delete ' ' (1 character) before '#'
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)

	wantEdited := "[pasted text 5 lines#1]"
	if m.input.Value() != wantEdited {
		t.Errorf("expected edited input %q, got %q", wantEdited, m.input.Value())
	}

	// Store entry should be invalidated by prunePastes
	if len(m.pastes) != 0 {
		t.Errorf("expected paste store to be empty after placeholder was edited, got %v", m.pastes)
	}

	// Submission now submits the edited text verbatim without expanding
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(m.entries) == 0 || m.entries[0].Content != wantEdited {
		t.Errorf("expected submission of verbatim edited text %q, got %+v", wantEdited, m.entries)
	}
}
