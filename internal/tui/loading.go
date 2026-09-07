package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Loading indicator constants. The indicator is a compact row of solid
// blocks in the bottom status area, inspired by OpenCode's block style.
// It is intentionally tiny: a fixed number of blocks (no layout shifts)
// with a soft red energy wave sweeping left-to-right then back.
const (
	// loadingBlockCount stays within the 5-10 block requirement.
	loadingBlockCount = 8
	// loadingBlockGlyph is the only glyph used; intensity is carried by
	// color, not by swapping glyphs or animation styles.
	loadingBlockGlyph = "■"
	// loadingTickInterval matches the previous spinner cadence (~10fps):
	// smooth enough to feel alive, slow enough to keep CPU negligible.
	loadingTickInterval = 100 * time.Millisecond
)

// loadingDimRGB is the low-intensity red for idle blocks. It sits in the
// same red family as colorAccent (#FF3B3B) but dark enough to read as
// background energy on a dark terminal.
const (
	loadingDimR, loadingDimG, loadingDimB         = 0x5A, 0x1A, 0x1A
	loadingBrightR, loadingBrightG, loadingBrightB = 0xFF, 0x3B, 0x3B
)

// loadingTickMsg advances the block wave one frame. It follows the same
// Bubble Tea Tick pattern the spinner used: the Update handler drops the
// tick when the run is no longer waiting, so the chain dies cleanly on
// completion, error, or cancellation without extra goroutines.
type loadingTickMsg struct{}

// loadingTickCmd schedules the next animation frame without blocking the
// event loop.
func loadingTickCmd() tea.Cmd {
	return tea.Tick(loadingTickInterval, func(time.Time) tea.Msg {
		return loadingTickMsg{}
	})
}

// loadingColorProfile is a variable for tests to force a profile without
// touching the global renderer.
var loadingColorProfile = func() termenv.Profile {
	return lipgloss.ColorProfile()
}

// loadingSupportsGradient reports whether the terminal can show the smooth
// red gradient. Only true color gets the animated wave; anything else
// degrades to static blocks in the existing accent style.
func loadingSupportsGradient() bool {
	return loadingColorProfile() == termenv.TrueColor
}

// loadingPosition maps a frame counter to a highlight position in
// [0, loadingBlockCount-1]. It is a triangle wave advancing half a block
// per tick: left-to-right, then smoothly back right-to-left, repeating.
// Half-steps plus the Gaussian falloff below keep the motion soft rather
// than jumping block-to-block.
func loadingPosition(frame int) float64 {
	span := float64(loadingBlockCount - 1)
	if span <= 0 {
		return 0
	}
	period := 2 * span
	d := float64(frame) * 0.5
	m := math.Mod(d, period)
	if m < 0 {
		m += period
	}
	if m > span {
		m = period - m
	}
	return m
}

// loadingIntensity converts a distance in blocks to a 0-1 glow amount.
// A Gaussian falloff gives smooth interpolation: the peak block hits the
// strongest accent, immediate neighbors glow partially, and distant blocks
// settle back to the dim base instead of snapping on/off.
func loadingIntensity(dist float64) float64 {
	if dist < 0 {
		dist = -dist
	}
	return math.Exp(-(dist * dist) / 1.5)
}

// loadingColor interpolates between the dim base red and the accent red.
// t=0 is the low-intensity block, t=1 is the strongest accent.
func loadingColor(t float64) lipgloss.Color {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	r := int(math.Round(float64(loadingDimR)*(1-t) + float64(loadingBrightR)*t))
	g := int(math.Round(float64(loadingDimG)*(1-t) + float64(loadingBrightG)*t))
	b := int(math.Round(float64(loadingDimB)*(1-t) + float64(loadingBrightB)*t))
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", r, g, b))
}

// renderLoadingBlocks returns one frame of the indicator. The output is
// always loadingBlockCount blocks attached with no separator, so every
// frame has identical cell width and the footer never shifts layout.
func renderLoadingBlocks(frame int) string {
	if !loadingSupportsGradient() {
		return loadingStaticBlocks()
	}
	pos := loadingPosition(frame)
	blocks := make([]string, 0, loadingBlockCount)
	for i := 0; i < loadingBlockCount; i++ {
		dist := math.Abs(float64(i) - pos)
		t := loadingIntensity(dist)
		style := lipgloss.NewStyle().Foreground(loadingColor(t))
		blocks = append(blocks, style.Render(loadingBlockGlyph))
	}
	return strings.Join(blocks, "")
}

// loadingStaticBlocks is the graceful fallback when true color is
// unavailable: same geometry, single accent style, no animation.
func loadingStaticBlocks() string {
	parts := make([]string, 0, loadingBlockCount)
	for i := 0; i < loadingBlockCount; i++ {
		parts = append(parts, loadingBlockGlyph)
	}
	return statusBusyStyle.Render(strings.Join(parts, ""))
}

// renderLoading renders the model's current loader frame. It is a thin
// method so renderFooter stays readable and tests can drive frames via
// model.loadingFrame directly.
func (m model) renderLoading() string {
	return renderLoadingBlocks(m.loadingFrame)
}
