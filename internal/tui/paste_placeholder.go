package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Paste placeholders: a bracketed paste is shown in the input as one
// compact token (`[pasted text 12 lines #3]`) instead of being rendered
// character by character. The original content is kept out-of-band on
// the model and reconstructed verbatim at submission.
//
// The token embeds a unique id because several pastes can live in one
// message: two pastes of the same size would otherwise produce
// identical tokens, and after the user edits or deletes one of them the
// surviving token could no longer be matched to its content by
// position. A unique token makes reconstruction exact after arbitrary
// editing, which is the contract that matters: submit the original,
// unmodified content.
//
// The store's lifetime is exactly the current input: entries are
// pruned whenever the input is cleared (submit, Esc, /new) or replaced
// (palette completion), so no paste outlives the message it belongs to.
const (
	// pasteTokenPrefix is the scan prefix of every placeholder token.
	// It is distinctive enough (bracket + exact words) that ordinary
	// typed text cannot collide with it by accident.
	pasteTokenPrefix = "[pasted text"
	// pastePlaceholderMinChars is the size at which a single-line
	// paste stops being inline text and becomes a placeholder. Short
	// single-line pastes (a filename, a URL, an error string) read and
	// edit naturally inline; the placeholder earns its keep once the
	// content stops being easy to hand-edit.
	pastePlaceholderMinChars = 64
)

// pasteLineCount reports how many lines the placeholder should name: a
// trailing newline closes the last line instead of opening an empty
// one, so "a\nb\n" counts as 2 lines.
func pasteLineCount(content string) int {
	if content == "" {
		return 1
	}
	return strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
}

// pastePlaceholder renders the compact token for a paste of n lines
// with the model's unique paste id. Singular/plural follows n.
func pastePlaceholder(n, id int) string {
	word := "lines"
	if n == 1 {
		word = "line"
	}
	return pasteTokenPrefix + " " + strconv.Itoa(n) + " " + word + " #" + strconv.Itoa(id) + "]"
}

// pasteDeservesPlaceholder reports whether a paste should collapse
// into a placeholder token: every multi-line paste does; a single-line
// paste does once it is long enough to be unwieldy inline.
func pasteDeservesPlaceholder(content string, lines int) bool {
	if lines > 1 {
		return true
	}
	return len(content) >= pastePlaceholderMinChars
}

// insertPaste handles one bracketed paste: the content either joins the
// input verbatim (short single-line pastes) or becomes a placeholder
// token backed by the store. The cursor ends up after whatever was
// inserted, exactly as before.
func (m *model) insertPaste(content string) {
	lines := pasteLineCount(content)
	if !pasteDeservesPlaceholder(content, lines) {
		m.input.InsertString(content)
		return
	}
	if m.pastes == nil {
		m.pastes = make(map[string]string)
	}
	m.pasteSeq++
	token := pastePlaceholder(lines, m.pasteSeq)
	m.pastes[token] = content
	m.input.InsertString(token)
}

// expandPastes reconstructs the original message: every placeholder
// token still present in value is replaced by the content it stored,
// left to right, one pass, non-recursively (a stored paste that itself
// contains token-shaped text is data, never re-expanded). Tokens with
// no matching entry — typed fragments, or tokens whose store entry was
// already pruned — pass through unchanged.
func expandPastes(value string, pastes map[string]string) string {
	if len(pastes) == 0 || !strings.Contains(value, pasteTokenPrefix) {
		return value
	}
	var b strings.Builder
	b.Grow(len(value))
	for {
		i := strings.Index(value, pasteTokenPrefix)
		if i < 0 {
			b.WriteString(value)
			return b.String()
		}
		b.WriteString(value[:i])
		rest := value[i:]
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			// No closing bracket: not a token this feature issued.
			b.WriteString(rest)
			return b.String()
		}
		token := rest[:end+1]
		if content, ok := pastes[token]; ok {
			b.WriteString(content)
		} else {
			b.WriteString(token)
		}
		value = rest[end+1:]
	}
}

// prunePastes drops every store entry whose token is no longer in the
// input, so a paste never outlives the message it belongs to. Called
// wherever the input is cleared or wholesale replaced.
func (m *model) prunePastes() {
	if len(m.pastes) == 0 {
		return
	}
	value := m.input.Value()
	for token := range m.pastes {
		if !strings.Contains(value, token) {
			delete(m.pastes, token)
		}
	}
}

// resetInput clears the input box and its paste store together: the
// store is only meaningful while the tokens it backs are still in the
// input.
func (m *model) resetInput() {
	m.input.Reset()
	m.prunePastes()
}

// deletePlaceholderAroundCursor inspects the current input line at the cursor.
// If isDelete is false (Backspace) and the cursor is immediately after a recognized
// paste placeholder token, that entire token is deleted and its payload is invalidated.
// If isDelete is true (Forward Delete) and the cursor is immediately before a recognized
// paste placeholder token, that entire token is deleted forward and its payload is invalidated.
// Returns true if a placeholder was deleted, or false if normal character deletion should proceed.
func (m *model) deletePlaceholderAroundCursor(isDelete bool) bool {
	if len(m.pastes) == 0 {
		return false
	}
	val := m.input.Value()
	if val == "" {
		return false
	}
	lines := strings.Split(val, "\n")
	row := m.input.Line()
	if row < 0 || row >= len(lines) {
		return false
	}
	lineRunes := []rune(lines[row])
	li := m.input.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	if col < 0 || col > len(lineRunes) {
		return false
	}

	if !isDelete {
		// Backspace: cursor immediately after placeholder (ends with ']')
		if col == 0 || lineRunes[col-1] != ']' {
			return false
		}
		prefix := string(lineRunes[:col])
		var targetToken string
		for token := range m.pastes {
			if strings.HasSuffix(prefix, token) {
				targetToken = token
				break
			}
		}
		if targetToken == "" {
			return false
		}
		delete(m.pastes, targetToken)
		tokenRunes := len([]rune(targetToken))
		for i := 0; i < tokenRunes; i++ {
			m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		}
		return true
	}

	// Forward Delete: cursor immediately before placeholder (starts with '[')
	if col >= len(lineRunes) || lineRunes[col] != '[' {
		return false
	}
	suffix := string(lineRunes[col:])
	var targetToken string
	for token := range m.pastes {
		if strings.HasPrefix(suffix, token) {
			targetToken = token
			break
		}
	}
	if targetToken == "" {
		return false
	}
	delete(m.pastes, targetToken)
	tokenRunes := len([]rune(targetToken))
	for i := 0; i < tokenRunes; i++ {
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyDelete})
	}
	return true
}
