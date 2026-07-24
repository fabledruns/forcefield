package tui

import (
	"fmt"
	"strings"
)

type role int

const (
	roleUser role = iota
	roleAssistant
	roleError
)

type chatEntry struct {
	Role    role
	Content string
}

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

func renderTranscript(entries []chatEntry, width int) string {
	if len(entries) == 0 {
		return helpStyle.Render("Type a message and press enter to start.")
	}

	rendered := make([]string, 0, len(entries))
	for _, e := range entries {
		rendered = append(rendered, e.render(width))
	}
	return strings.Join(rendered, "\n\n")
}