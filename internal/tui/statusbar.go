package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Crush-faithful bottom status bar.
//
// This is a 1:1 copy of Crush's status bar (internal/ui/styles + model/status.go):
// a full-width solid color strip, exactly one row high, with a brighter/darker
// badge block on the far left and dark monospace text. No icons, no borders,
// no rounding, no Forcefield accent substitution.
//
// Badge texts and color roles match Crush exactly:
//   - OKAY! / HEY!  -> green  (indicator Julep, message Guac)
//   - WARNING        -> yellow (indicator Mustard, message Zest)
//   - ERROR          -> red    (indicator Coral, message Sriracha)
//
// Colors are CharmTone hex values used by Crush:
//   - Pepper #201F26 (bgBase), Charcoal #3A3943 (bgSubtle), Iron #4D4C57 (bgOverlay)
//   - Butter #FFFAF1 (white)
//   - Julep #00FFB2 (green), Guac #12C78F (greenDark)
//   - Mustard #F5EF34 (yellow), Zest #E8FE96 (warning)
//   - Coral #FF577D (red), Sriracha #EB4268 (redDark)

// statusKind mirrors Crush's util.InfoType.
type statusKind int

const (
	statusInfo statusKind = iota
	statusSuccess
	statusWarn
	statusError
	statusUpdate
)

// statusNotice is the current bottom-strip notification. Nil means no
// notification, in which case the footer shows the normal help line
// (Crush draws help alone when msg.IsEmpty()).
type statusNotice struct {
	kind    statusKind
	message string
}

var (
	// CharmTone values, see Crush internal/ui/styles/styles.go.
	crushPepper   = lipgloss.Color("#201F26")
	crushCharcoal = lipgloss.Color("#3A3943")
	crushIron     = lipgloss.Color("#4D4C57")
	crushButter   = lipgloss.Color("#FFFAF1")

	crushJulep    = lipgloss.Color("#00FFB2")
	crushGuac     = lipgloss.Color("#12C78F")
	crushMustard  = lipgloss.Color("#F5EF34")
	crushZest     = lipgloss.Color("#E8FE96")
	crushCoral    = lipgloss.Color("#FF577D")
	crushSriracha = lipgloss.Color("#EB4268")
)

var (
	// Indicator styles: dark text on bright badge, Padding(0,1), Bold.
	// Crush: base.Foreground(bgSubtle).Background(green).Padding(0,1).Bold(true)
	statusSuccessIndicator = lipgloss.NewStyle().
				Foreground(crushCharcoal).
				Background(crushJulep).
				Padding(0, 1).
				Bold(true).
				SetString("OKAY!")
	statusInfoIndicator   = statusSuccessIndicator
	statusUpdateIndicator = statusSuccessIndicator.Copy().SetString("HEY!")
	statusWarnIndicator   = statusSuccessIndicator.Copy().
				Foreground(crushIron).
				Background(crushMustard).
				SetString("WARNING")
	statusErrorIndicator = statusSuccessIndicator.Copy().
				Foreground(crushPepper).
				Background(crushCoral).
				SetString("ERROR")

	// Message styles: dark text on darker strip, Padding(0,1), not bold.
	// Crush: base.Foreground(bgSubtle).Background(greenDark).Padding(0,1)
	statusSuccessMessage = lipgloss.NewStyle().
				Foreground(crushCharcoal).
				Background(crushGuac).
				Padding(0, 1)
	statusInfoMessage   = statusSuccessMessage
	statusUpdateMessage = statusSuccessMessage
	statusWarnMessage   = statusSuccessMessage.Copy().
				Foreground(crushIron).
				Background(crushZest)
	statusErrorMessage = statusSuccessMessage.Copy().
				Foreground(crushButter).
				Background(crushSriracha)
)

// statusStyles returns the Crush indicator/message style pair for a kind.
// Info and Success share Crush's OKAY! green; Update is HEY! green.
func statusStyles(kind statusKind) (lipgloss.Style, lipgloss.Style) {
	switch kind {
	case statusWarn:
		return statusWarnIndicator, statusWarnMessage
	case statusError:
		return statusErrorIndicator, statusErrorMessage
	case statusUpdate:
		return statusUpdateIndicator, statusUpdateMessage
	case statusInfo:
		return statusInfoIndicator, statusInfoMessage
	default: // statusSuccess and unknown fall back to OKAY! green
		return statusSuccessIndicator, statusSuccessMessage
	}
}

// setNotice shows a Crush-style bottom strip notification, replacing any
// previous one. Empty messages clear instead of showing a blank strip.
func (m *model) setNotice(kind statusKind, message string) {
	if strings.TrimSpace(message) == "" {
		m.notice = nil
		return
	}
	m.notice = &statusNotice{kind: kind, message: message}
}

// clearNotice hides the bottom strip, restoring the normal help line.
func (m *model) clearNotice() {
	m.notice = nil
}

// renderStatusBar renders one full-width Crush status row.
//
// Layout mirrors Crush's Status.Draw:
//   - single row, Padding(0,1) on both blocks, no borders
//   - badge on the far left, message fills the rest left-aligned
//   - newlines collapsed to spaces, truncated with "…" to the available
//     width (width minus badge minus message padding), then space-padded
//     so the strip is always exactly width cells wide with solid background.
func renderStatusBar(width int, kind statusKind, message string) string {
	if width <= 0 {
		return ""
	}
	indStyle, msgStyle := statusStyles(kind)

	ind := indStyle.String()
	indWidth := lipgloss.Width(ind)
	msgPad := msgStyle.GetPaddingLeft() + msgStyle.GetPaddingRight()
	avail := width - indWidth - msgPad
	if avail < 0 {
		avail = 0
	}
	// Crush collapses newlines before truncating.
	msg := strings.Join(strings.Split(message, "\n"), " ")
	msg = truncateCells(msg, avail)
	if w := lipgloss.Width(msg); w < avail {
		msg += strings.Repeat(" ", avail-w)
	}
	return ind + msgStyle.Render(msg)
}
