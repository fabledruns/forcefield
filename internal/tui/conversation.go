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
	roleActivity
	roleError
	roleSystem // output from a slash command, e.g. /help or /model
)

// chatEntry is one turn in the visible transcript: something the user
// typed, a reply from the model, or an error that happened while getting
// one. Errors are rendered inline rather than crashing the session.
type chatEntry struct {
	Role      role
	Content   string
	Streaming bool

	// Tool, when set on a roleActivity entry, holds the structured details
	// behind the one-line tool summary so it can be expanded (ctrl+e).
	Tool *toolRecord
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
		body := renderMarkdown(e.Content, width, e.Streaming)
		return fmt.Sprintf("%s\n%s", label, body)
	case roleActivity:
		if e.Tool != nil {
			style := toolStatusStyle(e.Tool)
			caret := IconCollapsed
			if e.Tool.expanded {
				caret = IconExpanded
			}
			line := fmt.Sprintf("%s %s", caret, e.Content)
			if e.Tool.expanded {
				return style.Width(width).Render(line + formatToolDetails(e.Tool, width))
			}
			return style.Width(width).Render(line)
		}
		return activityStyle.Width(width).Render(e.Content)
	case roleError:
		label = errorLabelStyle.Render("Error")
	case roleSystem:
		label = systemLabelStyle.Render("System")
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
