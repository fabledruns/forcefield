package tui

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// These tests drive a real tea.Program over pipes, so the bytes go through
// bubbletea's actual input parser and event loop - the closest thing to
// pressing keys in a terminal that an automated test can get. The model is
// built without a runtime, so submissions are exercised through slash
// commands (which need no provider) and newline insertion is verified
// directly on the input state.

// driveProgram starts the TUI with piped input, writes each chunk after
// delay, then quits with Ctrl+C and returns the final model.
func driveProgram(t *testing.T, delay time.Duration, chunks ...string) model {
	t.Helper()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("input pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("output pipe: %v", err)
	}
	go io.Copy(io.Discard, outR) // keep the renderer from blocking

	m := newInputModel()
	m.width, m.height = 80, 24
	m.ready = true
	m.layout()

	program := tea.NewProgram(m, tea.WithInput(inR), tea.WithOutput(outW))

	done := make(chan model, 1)
	go func() {
		final, err := program.Run()
		if err != nil {
			done <- m // caller asserts on state; report via timeout below
			return
		}
		done <- final.(model)
	}()

	go func() {
		for _, chunk := range chunks {
			time.Sleep(delay)
			if _, err := inW.WriteString(chunk); err != nil {
				return
			}
		}
		// Give the program time to process everything, then quit.
		time.Sleep(400 * time.Millisecond)
		inW.WriteString("\x03") // Ctrl+C
		time.Sleep(200 * time.Millisecond)
		inW.Close()
	}()

	select {
	case got := <-done:
		outW.Close()
		return got
	case <-time.After(15 * time.Second):
		t.Fatal("program did not exit after Ctrl+C")
		return m
	}
}

func TestIntegrationPlainEnterSubmits(t *testing.T) {
	m := driveProgram(t, 150*time.Millisecond, "/help", "\r")

	if m.input.Value() != "" {
		t.Errorf("input not consumed by Enter submit: %q", m.input.Value())
	}
	found := false
	for _, e := range m.entries {
		if e.Role == roleSystem {
			found = true
		}
	}
	if !found {
		t.Errorf("plain Enter did not dispatch the slash command; entries: %+v", m.entries)
	}
}

func TestIntegrationAltEnterInsertsNewline(t *testing.T) {
	m := driveProgram(t, 120*time.Millisecond, "one", "\x1b\r", "two")

	if got := m.input.Value(); got != "one\ntwo" {
		t.Errorf("input.Value() = %q, want %q (ESC CR must arrive as alt+enter)", got, "one\ntwo")
	}
	if len(m.entries) != 0 {
		t.Errorf("alt+enter submitted: %d entries", len(m.entries))
	}
}

func TestIntegrationLineFeedInsertsNewline(t *testing.T) {
	// Terminals whose Shift+Enter sends a bare line feed (0x0A) deliver
	// KeyCtrlJ, which the textarea's InsertNewline binding receives.
	m := driveProgram(t, 120*time.Millisecond, "a", "\n", "b")

	if got := m.input.Value(); got != "a\nb" {
		t.Errorf("input.Value() = %q, want %q", got, "a\nb")
	}
}

func TestIntegrationBracketedPastePreservesNewlines(t *testing.T) {
	pasted := "line one\nline two\nline three"
	m := driveProgram(t, 120*time.Millisecond, "\x1b[200~"+pasted+"\x1b[201~")

	// The paste collapses to its placeholder token; the real newlines
	// live in the stored original, byte-for-byte.
	got := m.input.Value()
	if !strings.Contains(got, "[pasted text 3 lines #") || !strings.HasSuffix(got, "]") {
		t.Errorf("input.Value() = %q, want a placeholder token for the paste", got)
	}
	if strings.Contains(got, "\\n") {
		t.Errorf("input contains literal backslash-n: %q", got)
	}
	if stored := m.pastes[got]; stored != pasted {
		t.Errorf("stored paste = %q, want pasted text with real newlines %q", stored, pasted)
	}
}

func TestIntegrationUnbracketedEnterSubmitsImmediately(t *testing.T) {
	// Without bracketed paste markers, an embedded CR is a plain Enter
	// keystroke and submits immediately without relying on timing heuristics.
	m := driveProgram(t, 100*time.Millisecond, "line one\rline two")

	if len(m.entries) == 0 || m.entries[0].Content != "line one" {
		t.Errorf("entries = %+v, want 'line one' submitted", m.entries)
	}
	if got := m.input.Value(); got != "line two" {
		t.Errorf("input.Value() = %q, want 'line two'", got)
	}
}
