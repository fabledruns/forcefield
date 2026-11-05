package tui

import (
	"fmt"
	"math"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// This file implements the deterministic opening-animation state machine
// described in PLAN.md §4/§5/§9: the once-per-process opening signature
// that expands the crosshair separators, reveals the FORCEFIELD wordmark
// at the center intersection, and fades it out at exactly 10.00s from
// the animation origin over 600ms.
//
// Every rendered value is a pure function of elapsed wall-clock time
// (Δt = now.Sub(startTime)): nothing counts ticks or sleeps, so a slow
// event loop can delay when a frame appears but never what it shows.
// The tick scheduling and model integration live in model_update.go;
// this file holds only the state, the timeline math, and the
// color-profile-aware fade curves so each can be tested in isolation.

// animPhase labels which opening phase Δt falls in. It is re-derived
// from elapsed time on every Advance rather than upgraded independently,
// so it can never drift ahead of the real clock. The only latched spark
// of state is wordmarkDismissed (see Advance).
type animPhase uint8

const (
	// animPhaseUnstarted is the zero value: Start has not latched an
	// origin yet, so every getter reports its neutral value.
	animPhaseUnstarted animPhase = iota
	// animPhaseSeparators covers 0.00s→0.55s: the vertical separator
	// expands from the center (0→350ms) and the horizontal one follows
	// (250→650ms, overlapping by 100ms).
	animPhaseSeparators
	// animPhaseWordmarkReveal covers 0.55s→0.90s: the central mask opens
	// and the FORCEFIELD glyph rows appear from the center outward.
	animPhaseWordmarkReveal
	// animPhaseLabelsReveal covers 0.90s→1.25s: the quadrant headers
	// appear in staggered 70ms steps.
	animPhaseLabelsReveal
	// animPhaseLiveWithWordmark covers 1.25s→10.00s: live quadrant
	// telemetry plus the anchored gradient wordmark until the fade
	// deadline.
	animPhaseLiveWithWordmark
	// animPhaseWordmarkFade covers 10.00s→10.60s.
	animPhaseWordmarkFade
	// animPhaseSteady is latched at ≥10.60s. It is terminal: Done()
	// reports true, the wordmark never returns, and callers stop
	// scheduling ticks (zero idle CPU, PLAN §12.1).
	animPhaseSteady
)

const (
	// openingTickInterval targets ~30 FPS for the opening window.
	openingTickInterval = 33 * time.Millisecond
	// vertExpandDuration: vertical separator expands 0.00s → 0.35s.
	vertExpandDuration = 350 * time.Millisecond
	// horizExpandStart/Duration: horizontal separator expands
	// 0.25s → 0.65s (100ms overlap with the vertical).
	horizExpandStart    = 250 * time.Millisecond
	horizExpandDuration = 400 * time.Millisecond
	// wordmarkRevealStart/Duration: central wordmark mask opens
	// 0.55s → 0.90s from the center outward.
	wordmarkRevealStart    = 550 * time.Millisecond
	wordmarkRevealDuration = 350 * time.Millisecond
	// labelsRevealStart/Duration/Stagger: quadrant headers appear
	// 0.90s → 1.25s, one label per 70ms step (main agent first).
	labelsRevealStart    = 900 * time.Millisecond
	labelsRevealDuration = 350 * time.Millisecond
	labelsRevealStagger  = 70 * time.Millisecond
	// wordmarkFadeStartDeadline is EXACT: the fade begins 10.00s from
	// animStartTime — never 10.00s after the reveal completes at 0.90s
	// (PLAN §5.1). wordmarkFadeDuration covers 10.00s → 10.60s.
	wordmarkFadeStartDeadline = 10 * time.Second
	wordmarkFadeDuration      = 600 * time.Millisecond
	// totalOpeningDuration is the full opening signature length
	// (10.60s); steady state starts at or beyond it.
	totalOpeningDuration = wordmarkFadeStartDeadline + wordmarkFadeDuration
)

// openingAnimState is the entire opening-animation model: the latched
// origin wall clock, the most recent elapsed, the derived phase, and the
// permanently latched wordmark dismissal. It is owned by the Bubble Tea
// update loop (Start/Advance only ever run from Update or from tests
// passing synthetic timestamps), so no locking is needed.
type openingAnimState struct {
	startTime time.Time
	// elapsed caches now.Sub(startTime) from the most recent Advance.
	// Getters read this cache so rendering doesn't need another clock
	// read between the tick and the View.
	elapsed time.Duration
	phase   animPhase
	// wordmarkDismissed is latched at Δt ≥ 10.60s and never cleared.
	// Neither /clear, /new, /sessions, resize, nor a new delegated run
	// may reset it (PLAN §5.3): the wordmark is strictly
	// once-per-process.
	wordmarkDismissed bool
}

// Start latches the animation origin once, at t = 0.00s (in production
// from the first WindowSizeMsg). Later calls are ignored — including
// calls after dismissal — so nothing in the TUI can restart or shift the
// opening timeline.
func (a *openingAnimState) Start(now time.Time) {
	if a.wordmarkDismissed {
		return
	}
	if !a.startTime.IsZero() {
		return
	}
	a.startTime = now
	a.elapsed = 0
	a.phase = animPhaseSeparators
}

// Started reports whether Start has ever latched an origin.
func (a *openingAnimState) Started() bool {
	return !a.startTime.IsZero()
}

// Advance folds one frame's wall-clock time into the state. now is
// explicit — the openingTickMsg timestamp in production, a synthetic
// time in tests — so nothing here reads the clock itself, sleeps, or
// counts increments, and the 10.00s deadline stays exact under the
// slowest event loop (PLAN §5.1). Past 10.60s the state latches into
// animPhaseSteady and wordmarkDismissed permanently.
func (a *openingAnimState) Advance(now time.Time) {
	if a.wordmarkDismissed || a.startTime.IsZero() {
		return
	}
	dt := now.Sub(a.startTime)
	if dt < 0 {
		dt = 0
	}
	a.elapsed = dt
	a.phase = animPhaseFor(dt)
	if dt >= totalOpeningDuration {
		a.wordmarkDismissed = true
		a.phase = animPhaseSteady
	}
}

// animPhaseFor maps elapsed time to its phase label. Thresholds mirror
// the timeline in PLAN §4 (values at boundaries belong to the later
// phase).
func animPhaseFor(dt time.Duration) animPhase {
	switch {
	case dt >= wordmarkFadeStartDeadline:
		return animPhaseWordmarkFade
	case dt >= labelsRevealStart+labelsRevealDuration:
		return animPhaseLiveWithWordmark
	case dt >= labelsRevealStart:
		return animPhaseLabelsReveal
	case dt >= wordmarkRevealStart:
		return animPhaseWordmarkReveal
	default:
		return animPhaseSeparators
	}
}

// Done reports the animation has fully finished. Once true, callers must
// stop scheduling ticks; Done is terminal.
func (a *openingAnimState) Done() bool {
	return a.wordmarkDismissed
}

// VertProgress is the normalized vertical-separator expansion
// (easeOutCubic of Δt/350ms, clamped): 0 at the origin, 1 from
// Δt ≥ 350ms.
func (a *openingAnimState) VertProgress() float64 {
	return easeOutCubic(clamp01(float64(a.elapsed) / float64(vertExpandDuration)))
}

// HorizProgress is the normalized horizontal-separator expansion
// (easeOutCubic of (Δt−250ms)/400ms, clamped): 0 before Δt = 250ms, 1
// from Δt ≥ 650ms.
func (a *openingAnimState) HorizProgress() float64 {
	return easeOutCubic(clamp01(float64(a.elapsed-horizExpandStart) / float64(horizExpandDuration)))
}

// WordmarkRevealProgress is the normalized central wordmark reveal
// (easeOutQuart of (Δt−550ms)/350ms, clamped): the mask opens from the
// center outward and completes at Δt = 0.90s.
func (a *openingAnimState) WordmarkRevealProgress() float64 {
	return easeOutQuart(clamp01(float64(a.elapsed-wordmarkRevealStart) / float64(wordmarkRevealDuration)))
}

// LabelVisible reports whether quadrant label idx has revealed. idx 0 is
// the main agent; each following label appears one 70ms stagger step
// later (0.90s, 0.97s, 1.04s, 1.11s for idx 0..3).
func (a *openingAnimState) LabelVisible(idx int) bool {
	if idx < 0 {
		return false
	}
	return a.elapsed >= labelsRevealStart+time.Duration(idx)*labelsRevealStagger
}

// WordmarkFadeAlpha is 1.0 until the exact 10.00s deadline measured from
// animStartTime — not from the reveal completing at 0.90s — then 1 −
// easeInOutQuad over the 600ms fade window (PLAN §5.1). Unstarted states
// report 1.0; callers render the wordmark only while Started and
// alpha > 0.
func (a *openingAnimState) WordmarkFadeAlpha() float64 {
	if a.elapsed < wordmarkFadeStartDeadline {
		return 1.0
	}
	p := clamp01(float64(a.elapsed-wordmarkFadeStartDeadline) / float64(wordmarkFadeDuration))
	return 1.0 - easeInOutQuad(p)
}

// wordmarkRampStops is the Forcefield pink ramp the gradient wave
// interpolates through (PLAN §4, phase 4): deep trailing edge #A81343 →
// accent #ED2663 → label pink #F4729A → specular highlight #FFB3C9 →
// deep again, so φ wraps seamlessly at 1.0.
var wordmarkRampStops = [5][3]int{
	{0xA8, 0x13, 0x43},
	{0xED, 0x26, 0x63},
	{0xF4, 0x72, 0x9A},
	{0xFF, 0xB3, 0xC9},
	{0xA8, 0x13, 0x43},
}

// wordmarkGradientPhase computes the gradient wave phase for the glyph
// cell at banner column x / row y after Δt: φ = mod(0.08x + 0.25y −
// 1.8Δt, 1) (Δt in seconds), the diagonal wave in PLAN §4.
func wordmarkGradientPhase(x, y int, dt time.Duration) float64 {
	return wrapPhase(float64(x)*0.08 + float64(y)*0.25 - 1.8*dt.Seconds())
}

// wordmarkGradientRGB interpolates the ramp at phase φ ∈ [0,1) across
// its four segments, rounding like the banner gradient helpers.
func wordmarkGradientRGB(phi float64) (int, int, int) {
	phi = wrapPhase(phi)
	seg := int(phi * 4)
	if seg > 3 {
		seg = 3
	}
	local := phi*4 - float64(seg)
	a := wordmarkRampStops[seg]
	b := wordmarkRampStops[seg+1]
	lerp := func(x, y int) int {
		return int(math.Round(float64(x)*(1-local) + float64(y)*local))
	}
	return lerp(a[0], b[0]), lerp(a[1], b[1]), lerp(a[2], b[2])
}

// wrapPhase folds φ into [0,1); negative phases come from the −1.8Δt
// term of the gradient wave and wrap the same as any other value.
func wrapPhase(phi float64) float64 {
	m := math.Mod(phi, 1)
	if m < 0 {
		m += 1
	}
	return m
}

// wordmarkTrueColor returns the wordmark color for a truecolor terminal:
// the gradient-wave color at φ scaled toward black by the fade alpha,
// ⌊α·channel⌋ (PLAN §5.2.1). α = 1 reproduces the wave exactly; α ≤ 0
// collapses the cell to #000000, at which point callers stop painting
// glyphs at all.
func wordmarkTrueColor(phi, alpha float64) lipgloss.Color {
	r, g, b := wordmarkGradientRGB(phi)
	a := clamp01(alpha)
	scale := func(c int) int {
		return int(a * float64(c))
	}
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", scale(r), scale(g), scale(b)))
}

// wordmarkANSI256 maps the fade alpha through the deterministic 7-step
// 256-color ramp of PLAN §5.2.2. The gradient's per-cell phase is not
// preserved by palette quantization, so while α = 1 the wordmark reads
// as 204 (#ff5f87) and steps down the ramp as the fade progresses; the
// end state stays byte-identical across restarts either way.
func wordmarkANSI256(alpha float64) lipgloss.Color {
	switch a := clamp01(alpha); {
	case a >= 0.85:
		return lipgloss.Color("204")
	case a >= 0.70:
		return lipgloss.Color("197")
	case a >= 0.55:
		return lipgloss.Color("161")
	case a >= 0.40:
		return lipgloss.Color("125")
	case a >= 0.25:
		return lipgloss.Color("88")
	case a >= 0.10:
		return lipgloss.Color("52")
	case a > 0:
		return lipgloss.Color("235")
	default:
		// α = 0 means the wordmark is unmounted. Callers must gate on
		// α > 0 before painting any wordmark cell; the empty color is a
		// sentinel that can't be meaningfully rendered if they don't.
		return lipgloss.Color("")
	}
}

// wordmarkFadeGlyph substitutes dissolving glyphs on profile-limited
// terminals (ANSI 16, ascii, NO_COLOR — PLAN §5.2.3): full blocks step
// down through ▓ (α < 0.75), ░ (α < 0.50), and · (α < 0.25). The ▀/▄
// half blocks carry half the ink, so they dissolve a tier earlier — they
// go straight to ░ or · and skip the heavier ▓. Every other rune passes
// through untouched.
func wordmarkFadeGlyph(g rune, alpha float64) rune {
	switch g {
	case '█':
		switch {
		case alpha >= 0.75:
			return '█'
		case alpha >= 0.50:
			return '▓'
		case alpha >= 0.25:
			return '░'
		default:
			return '·'
		}
	case '▀', '▄':
		switch {
		case alpha >= 0.75:
			return g
		case alpha >= 0.50:
			return '░'
		default:
			return '·'
		}
	}
	return g
}

var (
	wordmarkFadeBold   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	wordmarkFadeNormal = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	wordmarkFadeFaint  = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("5"))
	wordmarkFadeDot    = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("5"))
)

// wordmarkFadeStyle colors the glyph-dissolve tiers of PLAN §5.2.3:
// bold magenta while glyphs are full blocks, plain magenta for ▓, faint
// for ░ and ·. Terminals running NO_COLOR render through lipgloss's
// Ascii profile, which strips the color automatically and leaves the
// bare glyph substitution.
func wordmarkFadeStyle(alpha float64) lipgloss.Style {
	switch a := clamp01(alpha); {
	case a >= 0.75:
		return wordmarkFadeBold
	case a >= 0.50:
		return wordmarkFadeNormal
	case a >= 0.25:
		return wordmarkFadeFaint
	default:
		return wordmarkFadeDot
	}
}

// openingColorProfile is a variable so tests can force a color profile
// without touching the global renderer (same pattern as
// loadingColorProfile).
var openingColorProfile = func() termenv.Profile {
	return lipgloss.ColorProfile()
}

// clamp01 clamps x into [0,1].
func clamp01(x float64) float64 {
	switch {
	case x < 0:
		return 0
	case x > 1:
		return 1
	default:
		return x
	}
}

// easeOutCubic: 1 − (1−t)³. Fast start, soft landing.
func easeOutCubic(t float64) float64 {
	t = clamp01(t)
	return 1 - math.Pow(1-t, 3)
}

// easeOutQuart: 1 − (1−t)⁴. The wordmark reveal's accelerating pop.
func easeOutQuart(t float64) float64 {
	t = clamp01(t)
	return 1 - math.Pow(1-t, 4)
}

// easeInOutQuad: the symmetric fade easing of PLAN §5.1.
func easeInOutQuad(t float64) float64 {
	t = clamp01(t)
	if t < 0.5 {
		return 2 * t * t
	}
	return 1 - math.Pow(-2*t+2, 2)/2
}
