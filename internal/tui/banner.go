package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// asciiBanner is a compact custom block-letter rendering of "FORCEFIELD"
// in the style of Crush's wordmark: 3 rows tall, thin-line geometric glyphs
// built from █ ▀ ▄ with tight 1-column letter spacing (2 columns between
// words). Letterforms follow the Crush letterform vocabulary (see
// LetterC/LetterE/LetterR): ▄ opens top curves, █ carries verticals, ▀
// carries horizontals and feet. Narrow rows are padded so every line is
// exactly asciiBannerWidth columns wide by construction.
const asciiBanner = `
█▀▀▀▀ ▄▀▀▀▀▄ █▀▀▀▀▄ ▄▀▀▀▀ █▀▀▀▀ █▀▀▀▀ █ █▀▀▀▀ █     █▀▀▀▀▄
█▀▀▀▀ █    █ █▀▀▀▀▄ █     █▀▀▀▀ █▀▀▀▀ █ █▀▀▀▀ █     █    █
▀      ▀▀▀▀  ▀    ▀  ▀▀▀▀ ▀▀▀▀▀ ▀     ▀ ▀▀▀▀▀ ▀▀▀▀▀ ▀▀▀▀▀ `

// asciiBannerWidth is the fixed width of every line in asciiBanner.
// renderBanner falls back to a compact single-line title below this
// width so the splash degrades gracefully instead of wrapping.
const asciiBannerWidth = 58

const bannerTagline = "an ultra-fast, lightweight, local-first AI agent harness for running specialized agents"

// Version is the binary version shown under the wordmark, set at build
// time via ldflags (see Makefile LDFLAGS):
// go build -ldflags "-X forcefield/internal/tui.Version=v1.2.3"
var Version = "dev"

// bannerVersionLabel formats Version for the splash: release builds show
// "v1.2.3", unversioned builds show "dev".
func bannerVersionLabel() string {
	v := strings.TrimSpace(Version)
	if v == "" {
		return "dev"
	}
	if v == "dev" || strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

var (
	bannerTaglineStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

	bannerGutterStyle = lipgloss.NewStyle().
				Foreground(colorDim)
)

// bannerGradientRGB holds the wordmark gradient endpoints: Forcefield
// pink #ED2663 on the left into off-white #F2F2F2 on the right. Kept as
// plain ints so the per-rune lerp below stays dependency-free.
const (
	bannerFromR, bannerFromG, bannerFromB = 0xED, 0x26, 0x63
	bannerToR, bannerToG, bannerToB       = 0xF2, 0xF2, 0xF2
)

// bannerGradientColor interpolates between pink and off-white. t=0 is pure
// pink, t=1 is pure off-white; out-of-range input clamps.
func bannerGradientColor(t float64) lipgloss.Color {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	r := int(math.Round(float64(bannerFromR)*(1-t) + float64(bannerToR)*t))
	g := int(math.Round(float64(bannerFromG)*(1-t) + float64(bannerToG)*t))
	b := int(math.Round(float64(bannerFromB)*(1-t) + float64(bannerToB)*t))
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", r, g, b))
}

// renderGradientLine styles one wordmark line with a subtle left-to-right
// gradient from pink into off-white, one color per rune. All runes,
// including spacing, are preserved exactly and stay bold.
func renderGradientLine(line string) string {
	return renderGradientWith(line, true)
}

// renderVersionLine styles the version label with the same pink to
// off-white ramp, regular weight so it sits quieter than the bold
// wordmark above it.
func renderVersionLine(label string) string {
	return renderGradientWith(label, false)
}

// renderGradientWith is the shared per-rune gradient renderer.
func renderGradientWith(line string, bold bool) string {
	runes := []rune(line)
	n := len(runes)
	if n == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range runes {
		t := 0.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		style := lipgloss.NewStyle().Bold(bold).Foreground(bannerGradientColor(t))
		b.WriteString(style.Render(string(r)))
	}
	return b.String()
}

// bannerPrefixWidth is the gutter column budget: the "|" rail plus two
// spaces. Art and tagline text both start at the same column so the
// splash reads as one left-aligned block.
const bannerPrefixWidth = 3

// bannerArtRows is the block-letter art height by construction.
const bannerArtRows = 3

// renderBanner left-aligns the FORCEFIELD splash (art + version +
// tagline) with a straight "|" rail as left padding and one blank margin
// row on top. It's shown in place of the transcript only while the
// conversation is empty, see conversation.renderTranscript.
func renderBanner(width int) string {
	if width < asciiBannerWidth+bannerPrefixWidth {
		return renderCompactBanner(width)
	}

	artLines := nonEmptyArtLines(asciiBanner)
	// The art is 3 rows by construction; guard anyway so a future edit
	// can't index out of range and wrap instead of crashing.
	for len(artLines) < bannerArtRows {
		artLines = append(artLines, "")
	}

	rail := bannerGutterStyle.Render("| ")
	lines := make([]string, 0, bannerArtRows+6)
	lines = append(lines, "")
	for _, al := range artLines[:bannerArtRows] {
		lines = append(lines, rail+" "+renderGradientLine(al))
	}
	lines = append(lines, rail+" "+renderVersionLine(bannerVersionLabel()))
	lines = append(lines, bannerGutterStyle.Render("|"))
	for _, tl := range wrapBannerTagline(bannerTagline, width-bannerPrefixWidth) {
		lines = append(lines, bannerGutterStyle.Render("|")+"  "+bannerTaglineStyle.Render(tl))
	}
	return lipgloss.PlaceHorizontal(width, lipgloss.Left, strings.Join(lines, "\n"))
}

// renderCompactBanner is the narrow-terminal fallback: the plain word,
// bold and styled, left-aligned with the same straight "|" rail, version,
// and top margin instead of block-letter art that would wrap.
func renderCompactBanner(width int) string {
	title := renderGradientLine("FORCEFIELD")
	rail := bannerGutterStyle.Render("| ")
	lines := []string{"", rail + " " + title, rail + " " + renderVersionLine(bannerVersionLabel()), bannerGutterStyle.Render("|")}
	for _, tl := range wrapBannerTagline(bannerTagline, width-bannerPrefixWidth) {
		lines = append(lines, bannerGutterStyle.Render("|")+"  "+bannerTaglineStyle.Render(tl))
	}
	return lipgloss.PlaceHorizontal(width, lipgloss.Left, strings.Join(lines, "\n"))
}

// nonEmptyArtLines splits block-letter art into its content rows,
// dropping the leading/trailing blank lines from the raw literal.
func nonEmptyArtLines(art string) []string {
	raw := strings.Split(art, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// wrapBannerTagline word-wraps the tagline to maxWidth display cells so
// the long harness description stays inside narrow terminals instead of
// overflowing the left-aligned splash.
func wrapBannerTagline(s string, maxWidth int) []string {
	if maxWidth <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	lines := make([]string, 0, 2)
	cur := words[0]
	for _, w := range words[1:] {
		if lipgloss.Width(cur+" "+w) <= maxWidth {
			cur += " " + w
			continue
		}
		lines = append(lines, cur)
		cur = w
	}
	return append(lines, cur)
}
