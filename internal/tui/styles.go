// Package tui implements Forcefield's interactive terminal chat interface,
// built on Bubble Tea. It is a presentation layer only: every message the
// user sends is answered by calling the exact same runtime.Run that `ff
// run` uses. This package adds no memory, tools, or behavior the runtime
// doesn't already have, it just makes talking to it feel like a chat
// session instead of one command per question.
package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent    = lipgloss.Color("#7C6FF2") 
	colorAssistant = lipgloss.Color("#4FD6BE") 
	colorMuted     = lipgloss.Color("#6C7086") 
	colorError     = lipgloss.Color("#F38BA8")
	colorBorder    = lipgloss.Color("#3B3B4F") 
	colorText      = lipgloss.Color("#CDD6F4") 
)

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#0E0E12")).
			Background(colorAccent).
			Padding(0, 1)

	headerMetaStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	userLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	assistantLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAssistant)

	errorLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorError)

	messageBodyStyle = lipgloss.NewStyle().
				Foreground(colorText)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(colorBorder).
				Padding(0, 1)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(colorAccent)
)