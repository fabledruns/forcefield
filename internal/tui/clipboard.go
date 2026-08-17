package tui

import (
	"encoding/base64"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// copyLastAssistantMessage returns a tea.Cmd that copies the most recent
// assistant reply to the terminal clipboard via OSC 52. OSC 52 works over
// SSH and in every mainstream terminal emulator, needs no clipboard
// daemon, and emits no visible text, so it composes safely with Bubble
// Tea's renderer. Terminals that don't support it simply ignore the
// sequence. Native click-drag selection (with Shift where the terminal
// requires it to bypass mouse-cell reporting) remains available as always.
//
// This deliberately doesn't add a clipboard dependency: the whole protocol
// is one escape sequence around base64.
func copyLastAssistantMessage(entries []chatEntry) tea.Cmd {
	return func() tea.Msg {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Role == roleAssistant && strings.TrimSpace(entries[i].Content) != "" {
				// Best-effort: a failed write only means the terminal
				// doesn't support OSC 52; there's nothing to recover from.
				_, _ = os.Stdout.WriteString(osc52Sequence(entries[i].Content))
				return nil
			}
		}
		return nil
	}
}

// osc52Sequence builds the OSC 52 "set clipboard" escape sequence for text.
func osc52Sequence(text string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(text, "\r\n", "\n")))
	return "\x1b]52;c;" + encoded + "\x07"
}
