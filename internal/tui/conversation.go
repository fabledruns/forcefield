package tui

import (
	"fmt"
	"strings"
)

// role identifies who authored a chatEntry.
type role int

const (
	roleUser role = iota
	roleAssistant
	roleError
)

// chatEntry is one turn in the visible transcript: something the user
// typed, a reply from the model, or an error that happened while getting
// one. Errors are rendered inline rather than crashing the session.
type chatEntry struct {
	Role    role
	Content string
}

// render turns a chatEntry into its final styled, word-wrapped form.
// width is the available content width in the viewport.
func (e chatEntry) render(width int) string {
	var label string
	switch e.Role {
	case roleUser:
		label = userLabelStyle.Render("You")
	case roleAssistant:
		label = assistantLabelStyle.Render("Forcefield")
	case roleError:
		label = errorLabelStyle.Render("Error")
	}

	body := messageBodyStyle.Width(width).Render(e.Content)
	return fmt.Sprintf("%s\n%s", label, body)
}

// renderTranscript joins the full entry history into the text shown in
// the scrollable viewport. Before the first message, it shows the
// FORCEFIELD splash instead of an empty pane.
func renderTranscript(entries []chatEntry, width int) string {
	if len(entries) == 0 {
		return renderBanner(width)
	}

	rendered := make([]string, 0, len(entries))
	for _, e := range entries {
		rendered = append(rendered, e.render(width))
	}
	return strings.Join(rendered, "\n\n")
}
