// Package tui implements the interactive terminal chat interface (presentation
// only; same runtime.Run as `ff run`). See docs/TUI.md.
package tui

import (
	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/runtime"
)

var (
	colorAccent    = lipgloss.Color("#ED2663")
	colorAssistant = lipgloss.Color("#ED2663")
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

	// System-output hierarchy (see system.go): keys stay quiet, values
	// read bright; /command tokens borrow the palette pink; diff rows
	// take the shared success/error hues.
	sysKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorMuted)

	sysNameStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	sysAddedStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)

	sysRemovedStyle = lipgloss.NewStyle().
			Foreground(colorError)

	sysHunkStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	// Inline semantic elements (see system.go): emphasis for headings and
	// code, underline for navigable references (paths and URLs share the
	// affordance), bold digits for scannable numbers, semantic hues for
	// status, and dim rules for separators.
	sysEmphStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorText)

	sysRefStyle = lipgloss.NewStyle().
			Underline(true).
			Foreground(colorText)

	sysNumStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorText)

	sysOkStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)

	sysWarnStyle = lipgloss.NewStyle().
			Foreground(colorWarning)

	sysFailStyle = lipgloss.NewStyle().
			Foreground(colorError)

	sysSepStyle = lipgloss.NewStyle().
			Foreground(colorDim)

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

	// Successful tool rows render in neutral gray: the outcome is
	// carried by the ◈/* glyph, not by a bright status color that
	// fights the Forcefield accent.
	toolSuccessStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

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

	pickerDetailStyle = lipgloss.NewStyle().
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
	// paletteActiveStyle highlights the selected command palette row in
	// Forcefield pink. Names stay quiet otherwise; descriptions are
	// always muted. No borders, no fills.
	paletteActiveStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)

	paletteNameStyle = lipgloss.NewStyle().
				Foreground(colorText)

	paletteDescStyle = lipgloss.NewStyle().
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
