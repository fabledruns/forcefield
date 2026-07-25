package tui

import "github.com/charmbracelet/lipgloss"

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

	bannerTaglineStyle = lipgloss.NewStyle().
				Foreground(colorMuted)
)

// renderBanner centers the FORCEFIELD splash (art + tagline) within the
// given width. It's shown in place of the transcript only while the
// conversation is empty, see conversation.renderTranscript.
func renderBanner(width int) string {
	if width < asciiBannerWidth {
		return renderCompactBanner(width)
	}

	block := bannerStyle.Render(asciiBanner) + "\n\n" + bannerTaglineStyle.Render(bannerTagline)
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, block)
}

// renderCompactBanner is the narrow-terminal fallback: the plain word,
// bold and styled, instead of block-letter art that would wrap.
func renderCompactBanner(width int) string {
	block := bannerStyle.Render("FORCEFIELD") + "\n" + bannerTaglineStyle.Render(bannerTagline)
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, block)
}
