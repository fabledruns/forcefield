// Package tui implements Forcefield's interactive terminal chat interface,
// built on Bubble Tea. It is a presentation layer only: every message the
// user sends is answered by calling the exact same runtime.Run that `ff
// run` uses. This package adds no memory, tools, or behavior the runtime
// doesn't already have, it just makes talking to it feel like a chat
// session instead of one command per question.
package tui

import (
	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/runtime"
)

var (
	colorAccent    = lipgloss.Color("#FF3B3B")
	colorAssistant = lipgloss.Color("#FF3B3B")
	colorMuted     = lipgloss.Color("#7A7A7A")
	colorDim       = lipgloss.Color("#4A4A50")
	colorError     = lipgloss.Color("#FF6B6B")
	colorSuccess   = lipgloss.Color("#7D9B76")
	colorWarning   = lipgloss.Color("#C4A35A")
	colorBorder    = lipgloss.Color("#3A3A40")
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

	headerSepStyle = lipgloss.NewStyle().
			Foreground(colorDim)

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

	thinkStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	thinkStepStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	toolNameStyle = lipgloss.NewStyle().
			Foreground(colorText)

	toolDetailStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	toolRunningStyle = lipgloss.NewStyle().
				Foreground(colorAccent)

	toolSuccessStyle = lipgloss.NewStyle().
				Foreground(colorSuccess)

	toolFailedStyle = lipgloss.NewStyle().
			Foreground(colorError)

	toolCancelStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	statusIdleStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	statusBusyStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	statusErrorStyle = lipgloss.NewStyle().
				Foreground(colorError)

	statusWarnStyle = lipgloss.NewStyle().
			Foreground(colorWarning)

	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), true, false, true, false).
				BorderForeground(colorBorder).
				Padding(0, 1)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	// hoverEmphasisStyle marks the transcript block currently under the
	// pointer: an underline on the block's summary line, nothing more.
	hoverEmphasisStyle = lipgloss.NewStyle().
				Underline(true).
				Foreground(colorText)

	// permOptionHoverStyle highlights the permission answer label under
	// the pointer.
	permOptionHoverStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)
)

var (
	pickerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
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

var (
	permissionQuestionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)

	permissionHelpStyle = lipgloss.NewStyle().
				Foreground(colorMuted)
)

var (
	suggestionListStyle = lipgloss.NewStyle().
				Foreground(colorAccent)

	suggestionPreviewStyle = lipgloss.NewStyle().
				Foreground(colorMuted)
)

func toolStatusStyle(t *toolRecord) lipgloss.Style {
	if t == nil || !t.finished {
		return toolRunningStyle
	}
	switch t.eventType {
	case runtime.EventToolCancelled:
		return toolCancelStyle
	case runtime.EventToolFailed, runtime.EventToolDenied:
		return toolFailedStyle
	}
	if t.err != "" {
		return toolFailedStyle
	}
	return toolSuccessStyle
}
