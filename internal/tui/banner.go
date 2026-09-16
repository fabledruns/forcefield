package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// asciiBanner is a hand-built block-letter rendering of "FORCEFIELD",
// shown as a splash screen before the first message is sent. Every line is
// exactly asciiBannerWidth columns wide by construction.
const asciiBanner = `
███████╗ ██████╗ ██████╗  ██████╗███████╗███████╗██╗███████╗██╗     ██████╗
██╔════╝██╔═══██╗██╔══██╗██╔════╝██╔════╝██╔════╝██║██╔════╝██║     ██╔══██╗
█████╗  ██║   ██║██████╔╝██║     █████╗  █████╗  ██║█████╗  ██║     ██║  ██║
██╔══╝  ██║   ██║██╔══██╗██║     ██╔══╝  ██╔══╝  ██║██╔══╝  ██║     ██║  ██║
██║     ╚██████╔╝██║  ██║╚██████╗███████╗██║     ██║███████╗███████╗██████╔╝
╚═╝      ╚═════╝ ╚═╝  ╚═╝ ╚═════╝╚══════╝╚═╝     ╚═╝╚══════╝╚══════╝╚═════╝`

// asciiBannerWidth is the fixed width of every line in asciiBanner.
// renderBanner falls back to a compact single-line title below this
// width so the splash degrades gracefully instead of wrapping.
const asciiBannerWidth = 68

const bannerTagline = "the local-first agent harness"

var (
	bannerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	bannerFieldStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorText)

	bannerTaglineStyle = lipgloss.NewStyle().
				Foreground(colorMuted)
)

// bannerForceWidth is the rune index where the FORCE half of the block-letter
// art ends and the FIELD half begins. FORCE (F,O,R,C,E) uses only wide glyphs
// while FIELD contains the narrow I and L, so the word boundary sits past the
// halfway point of each line. Block letters touch without uniform gaps, so a
// single vertical cut is approximate by one column across rows, but all runes
// are preserved and only the styling changes.
const bannerForceWidth = 41

// renderTwoToneLine styles the FORCE half of one banner line with the accent
// and the FIELD half with the default foreground.
func renderTwoToneLine(line string) string {
	runes := []rune(line)
	at := bannerForceWidth
	if at < 0 {
		at = 0
	}
	if at > len(runes) {
		at = len(runes)
	}
	return bannerStyle.Render(string(runes[:at])) + bannerFieldStyle.Render(string(runes[at:]))
}

// renderTwoToneBanner applies the FORCE/FIELD split to every non-empty line
// of the art, preserving all runes and line breaks exactly.
func renderTwoToneBanner(art string) string {
	lines := strings.Split(art, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = renderTwoToneLine(line)
	}
	return strings.Join(lines, "\n")
}

// renderBanner centers the FORCEFIELD splash (art + tagline) within the
// given width. It's shown in place of the transcript only while the
// conversation is empty, see conversation.renderTranscript.
func renderBanner(width int) string {
	if width < asciiBannerWidth {
		return renderCompactBanner(width)
	}

	block := renderTwoToneBanner(asciiBanner) + "\n\n" + bannerTaglineStyle.Render(bannerTagline)
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, block)
}

// renderCompactBanner is the narrow-terminal fallback: the plain word,
// bold and styled, instead of block-letter art that would wrap.
func renderCompactBanner(width int) string {
	block := bannerStyle.Render("FORCE") + bannerFieldStyle.Render("FIELD") + "\n" + bannerTaglineStyle.Render(bannerTagline)
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, block)
}
