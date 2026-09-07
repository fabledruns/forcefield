package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// forceLoadingProfile swaps the color-profile probe for the duration of a
// test, so gradient vs. static rendering is deterministic regardless of
// the CI terminal. It also forces lipgloss's renderer, otherwise CI (Ascii,
// no TTY) would strip the gradient ANSI codes and every frame would look
// identical even when the logic differs.
func forceLoadingProfile(p termenv.Profile) func() {
	prevProbe := loadingColorProfile
	loadingColorProfile = func() termenv.Profile { return p }
	prevRenderer := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	return func() {
		loadingColorProfile = prevProbe
		lipgloss.SetColorProfile(prevRenderer)
	}
}

func TestLoadingBlockCountInRange(t *testing.T) {
	if loadingBlockCount < 5 || loadingBlockCount > 10 {
		t.Errorf("loadingBlockCount = %d, want 5-10", loadingBlockCount)
	}
}

func TestLoadingPositionSweepsAndReturns(t *testing.T) {
	restore := forceLoadingProfile(termenv.TrueColor)
	defer restore()

	if got := loadingPosition(0); got != 0 {
		t.Errorf("loadingPosition(0) = %v, want 0", got)
	}
	// Half a block per tick: frame 14 reaches the far end (span 7).
	if got := loadingPosition(14); got != float64(loadingBlockCount-1) {
		t.Errorf("loadingPosition(14) = %v, want %v", got, float64(loadingBlockCount-1))
	}
	// Full ping-pong period returns to the start.
	if got := loadingPosition(28); got != 0 {
		t.Errorf("loadingPosition(28) = %v, want 0", got)
	}
	// Midpoint of the return leg heads back left.
	mid := loadingPosition(21)
	if mid <= 0 || mid >= float64(loadingBlockCount-1) {
		t.Errorf("loadingPosition(21) = %v, want interior point on return leg", mid)
	}
}

func TestLoadingIntensityIsSmooth(t *testing.T) {
	if got := loadingIntensity(0); got != 1 {
		t.Errorf("loadingIntensity(0) = %v, want 1", got)
	}
	prev := 2.0
	for _, d := range []float64{0, 0.5, 1, 1.5, 2, 3} {
		got := loadingIntensity(d)
		if got < 0 || got > 1 {
			t.Errorf("loadingIntensity(%v) = %v, want in [0,1]", d, got)
		}
		if got >= prev {
			t.Errorf("loadingIntensity(%v) = %v, want strictly decreasing from %v", d, got, prev)
		}
		prev = got
	}
	// Neighbors glow partially instead of snapping off.
	if got := loadingIntensity(1); got < 0.3 || got > 0.7 {
		t.Errorf("loadingIntensity(1) = %v, want soft partial glow (~0.5)", got)
	}
}

func TestLoadingColorUsesAccentSystem(t *testing.T) {
	if got := loadingColor(1); string(got) != "#FF3B3B" {
		t.Errorf("loadingColor(1) = %q, want accent #FF3B3B", got)
	}
	if got := loadingColor(0); string(got) != "#5A1A1A" {
		t.Errorf("loadingColor(0) = %q, want dim #5A1A1A", got)
	}
	mid := string(loadingColor(0.5))
	if mid == "#FF3B3B" || mid == "#5A1A1A" {
		t.Errorf("loadingColor(0.5) = %q, want interpolated midpoint", mid)
	}
}

func TestRenderLoadingBlocksStableWidth(t *testing.T) {
	restore := forceLoadingProfile(termenv.TrueColor)
	defer restore()

	var width int
	wantAttached := strings.Repeat(loadingBlockGlyph, loadingBlockCount)
	for _, frame := range []int{0, 1, 7, 14, 21, 28, 100} {
		plain := stripANSI(renderLoadingBlocks(frame))
		if got := strings.Count(plain, loadingBlockGlyph); got != loadingBlockCount {
			t.Fatalf("frame %d has %d blocks, want %d (%q)", frame, got, loadingBlockCount, plain)
		}
		if plain != wantAttached {
			t.Errorf("frame %d = %q, want attached blocks %q (no spaces)", frame, plain, wantAttached)
		}
		// No spinner glyphs, dots, or ASCII art may leak in.
		for _, bad := range []string{"⠋", "⠙", "●", "...", "|", "/", "-", "\\"} {
			if strings.Contains(plain, bad) {
				t.Errorf("frame %d contains spinner fragment %q (%q)", frame, bad, plain)
			}
		}
		w := len([]rune(plain))
		if width == 0 {
			width = w
		} else if w != width {
			t.Errorf("frame %d width = %d runes, want stable %d (%q)", frame, w, width, plain)
		}
	}
}

func TestRenderLoadingBlocksAnimate(t *testing.T) {
	restore := forceLoadingProfile(termenv.TrueColor)
	defer restore()

	// Raw (ANSI) frames must differ as the highlight sweeps; stripped
	// geometry stays identical (covered above).
	if renderLoadingBlocks(0) == renderLoadingBlocks(7) {
		t.Error("frames 0 and 7 render identically, want moving highlight")
	}
}

func TestLoadingDegradesToStaticBlocks(t *testing.T) {
	for _, p := range []termenv.Profile{termenv.ANSI, termenv.ANSI256, termenv.Ascii} {
		restore := forceLoadingProfile(p)
		a := renderLoadingBlocks(0)
		b := renderLoadingBlocks(9)
		restore()
		if a != b {
			t.Errorf("profile %v animates, want static blocks", p)
		}
		plain := stripANSI(a)
		if got := strings.Count(plain, loadingBlockGlyph); got != loadingBlockCount {
			t.Errorf("profile %v static has %d blocks, want %d (%q)", p, got, loadingBlockCount, plain)
		}
	}
}

func TestLoadingTickAdvancesOnlyWhenWaiting(t *testing.T) {
	restore := forceLoadingProfile(termenv.TrueColor)
	defer restore()

	m := sizedModel()
	m.waiting = true
	m.loadingFrame = 3
	next, cmd := m.Update(loadingTickMsg{})
	got := next.(model)
	if got.loadingFrame != 4 {
		t.Errorf("loadingFrame = %d, want 4", got.loadingFrame)
	}
	if cmd == nil {
		t.Error("active loader returned nil cmd, want scheduled next tick")
	}

	idle := sizedModel()
	idle.waiting = false
	idle.loadingFrame = 3
	next, cmd = idle.Update(loadingTickMsg{})
	got = next.(model)
	if got.loadingFrame != 3 {
		t.Errorf("idle loadingFrame = %d, want unchanged 3", got.loadingFrame)
	}
	if cmd != nil {
		t.Error("idle loader scheduled a tick, want clean stop")
	}
}

func TestLoadingTickStopsWhenDegraded(t *testing.T) {
	restore := forceLoadingProfile(termenv.Ascii)
	defer restore()

	m := sizedModel()
	m.waiting = true
	_, cmd := m.Update(loadingTickMsg{})
	if cmd != nil {
		t.Error("degraded loader scheduled a tick, want static with no animation loop")
	}
}

func TestStopStreamResetsLoader(t *testing.T) {
	m := startTestStream(t)
	m.loadingFrame = 12
	m.stopStream(true)
	if m.waiting {
		t.Error("waiting still set after stopStream")
	}
	if m.loadingFrame != 0 {
		t.Errorf("loadingFrame = %d, want 0 after clean stop", m.loadingFrame)
	}
}

func TestFooterShowsBlocksWhileWaiting(t *testing.T) {
	restore := forceLoadingProfile(termenv.TrueColor)
	defer restore()

	m := sizedModel()
	m.waiting = true
	m.status = "Thinking"
	footer := stripANSI(m.renderFooter())
	if got := strings.Count(footer, loadingBlockGlyph); got != loadingBlockCount {
		t.Errorf("footer has %d blocks, want %d (%q)", got, loadingBlockCount, footer)
	}
	if !strings.Contains(footer, "Thinking") {
		t.Errorf("footer lost activity label (%q)", footer)
	}

	m.showActivity = false
	m.status = "Thinking"
	footer = stripANSI(m.renderFooter())
	if got := strings.Count(footer, loadingBlockGlyph); got != loadingBlockCount {
		t.Errorf("activity-hidden footer has %d blocks, want %d (%q)", got, loadingBlockCount, footer)
	}
}
