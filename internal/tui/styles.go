// Package tui implements Forcefield's interactive terminal chat interface,
// built on Bubble Tea. It is a presentation layer only: every message the
// user sends is answered by calling the exact same runtime.Run that `ff
// run` uses. This package adds no memory, tools, or behavior the runtime
// doesn't already have, it just makes talking to it feel like a chat
// session instead of one command per question.
package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent    = lipgloss.Color("#FF3B3B")
	colorAssistant = lipgloss.Color("#FF3B3B")
	colorMuted     = lipgloss.Color("#7A7A7A")
	colorError     = lipgloss.Color("#FF6B6B")
	colorBorder    = lipgloss.Color("#f8a7a7")
	colorText      = lipgloss.Color("#EAEAEA")
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

	systemLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorMuted)

	messageBodyStyle = lipgloss.NewStyle().
				Foreground(colorText)

	activityStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(colorBorder).
				Padding(0, 1)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(colorAccent)
)

var (
	pickerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorder).
				Padding(1, 2)

	pickerTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)

	pickerActiveStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)

	pickerSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorText).
				Background(lipgloss.Color("#3A1414"))

	pickerMetaStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

	pickerHelpStyle = lipgloss.NewStyle().
				Foreground(colorMuted)
)