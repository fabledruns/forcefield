package tui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// animTestOrigin is a fixed synthetic animation origin: tests feed it to
// Start/Advance directly, so every timing assertion below is exact and
// the suite never sleeps or depends on real scheduling.
var animTestOrigin = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// newOpeningAnimForTest returns an animation whose origin is latched at
// animTestOrigin.
func newOpeningAnimForTest() *openingAnimState {
	a := &openingAnimState{}
	a.Start(animTestOrigin)
	return a
}

// withOpeningProfile forces the animation color profile for the
// duration of a test (both the probe the fade math consults and the
// global renderer lipgloss styles resolve through), mirroring the
// loading-test pattern.
func withOpeningProfile(t *testing.T, p termenv.Profile) {
	t.Helper()
	prevProbe := openingColorProfile
	openingColorProfile = func() termenv.Profile { return p }
	prevRenderer := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() {
		openingColorProfile = prevProbe
		lipgloss.SetColorProfile(prevRenderer)
	})
}

// TestOpeningAnimTimelineSequence pins the PLAN §4 timeline with exact,
// binary-representable progress values at each phase boundary.
func TestOpeningAnimTimelineSequence(t *testing.T) {
	a := newOpeningAnimForTest()

	// t = 0ms: blank origin — separators at zero length, no labels.
	a.Advance(animTestOrigin)
	if a.phase != animPhaseSeparators {
		t.Errorf("phase at t=0: got %v, want animPhaseSeparators", a.phase)
	}
	if a.VertProgress() != 0 || a.HorizProgress() != 0 || a.WordmarkRevealProgress() != 0 {
		t.Errorf("progresses at t=0: got vert=%v horiz=%v reveal=%v, want all 0",
			a.VertProgress(), a.HorizProgress(), a.WordmarkRevealProgress())
	}
	if a.LabelVisible(0) {
		t.Error("main agent label visible at t=0")
	}

	// t = 175ms: vertical half expanded (easeOutCubic(0.5) = 0.875),
	// horizontal not started.
	a.Advance(animTestOrigin.Add(175 * time.Millisecond))
	if a.VertProgress() != 0.875 {
		t.Errorf("VertProgress at t=175ms: got %v, want 0.875", a.VertProgress())
	}
	if a.HorizProgress() != 0 {
		t.Errorf("HorizProgress at t=175ms: got %v, want 0 (Δt < 250ms)", a.HorizProgress())
	}

	// t = 450ms: vertical complete, horizontal half expanded.
	a.Advance(animTestOrigin.Add(450 * time.Millisecond))
	if a.VertProgress() != 1 {
		t.Errorf("VertProgress at t=450ms: got %v, want 1", a.VertProgress())
	}
	if a.HorizProgress() != 0.875 {
		t.Errorf("HorizProgress at t=450ms: got %v, want 0.875", a.HorizProgress())
	}
	if a.WordmarkRevealProgress() != 0 {
		t.Errorf("WordmarkRevealProgress at t=450ms: got %v, want 0 (Δt < 550ms)",
			a.WordmarkRevealProgress())
	}

	// t = 725ms: wordmark mask opening.
	a.Advance(animTestOrigin.Add(725 * time.Millisecond))
	if a.phase != animPhaseWordmarkReveal {
		t.Errorf("phase at t=725ms: got %v, want animPhaseWordmarkReveal", a.phase)
	}
	if a.WordmarkRevealProgress() != 0.9375 {
		t.Errorf("WordmarkRevealProgress at t=725ms: got %v, want 0.9375 (easeOutQuart(0.5))",
			a.WordmarkRevealProgress())
	}

	// t = 900ms: reveal complete, main agent label just visible.
	a.Advance(animTestOrigin.Add(900 * time.Millisecond))
	if a.WordmarkRevealProgress() != 1 {
		t.Errorf("WordmarkRevealProgress at t=900ms: got %v, want 1", a.WordmarkRevealProgress())
	}
	if a.phase != animPhaseLabelsReveal {
		t.Errorf("phase at t=900ms: got %v, want animPhaseLabelsReveal", a.phase)
	}
	if !a.LabelVisible(0) {
		t.Error("main agent label not visible at t=900ms")
	}
	if a.LabelVisible(1) {
		t.Error("subagent-1 label visible at t=900ms (stagger not reached)")
	}

	// The 70ms stagger: label 3 appears at 900ms + 3·70ms = 1110ms.
	a.Advance(animTestOrigin.Add(1109 * time.Millisecond))
	if a.LabelVisible(3) {
		t.Error("subagent-3 label visible at t=1109ms, one millisecond early")
	}
	a.Advance(animTestOrigin.Add(1110 * time.Millisecond))
	if !a.LabelVisible(3) {
		t.Error("subagent-3 label not visible at t=1110ms")
	}

	// t = 1250ms: all labels live; fade still untouched.
	a.Advance(animTestOrigin.Add(1250 * time.Millisecond))
	for idx := 0; idx < 4; idx++ {
		if !a.LabelVisible(idx) {
			t.Errorf("label %d not visible at t=1250ms", idx)
		}
	}
	if a.phase != animPhaseLiveWithWordmark {
		t.Errorf("phase at t=1250ms: got %v, want animPhaseLiveWithWordmark", a.phase)
	}
	if a.WordmarkFadeAlpha() != 1.0 {
		t.Errorf("WordmarkFadeAlpha at t=1250ms: got %v, want 1.0", a.WordmarkFadeAlpha())
	}
}

// TestWordmarkFadeStartsAtExactTenSecondDeadline pins the non-negotiable
// deadline: the fade is measured from animStartTime, not from reveal
// completion (PLAN §5.1).
func TestWordmarkFadeStartsAtExactTenSecondDeadline(t *testing.T) {
	a := newOpeningAnimForTest()

	a.Advance(animTestOrigin.Add(9999 * time.Millisecond))
	if a.phase != animPhaseLiveWithWordmark {
		t.Errorf("phase at t=9999ms: got %v, want animPhaseLiveWithWordmark", a.phase)
	}
	if a.WordmarkFadeAlpha() != 1.0 {
		t.Errorf("WordmarkFadeAlpha at t=9999ms: got %v, want 1.0", a.WordmarkFadeAlpha())
	}
	if a.Done() {
		t.Error("Done at t=9999ms")
	}
	// 9999ms is 9099ms after the reveal completed — the deadline is
	// anchored to the origin, not to the reveal.
	if a.WordmarkRevealProgress() != 1 {
		t.Errorf("reveal complete at t=9999ms: got %v, want 1", a.WordmarkRevealProgress())
	}

	// At exactly 10.00s the fade phase begins; α is still 1.0 because
	// the fade curve starts easing from full opacity.
	a.Advance(animTestOrigin.Add(10000 * time.Millisecond))
	if a.phase != animPhaseWordmarkFade {
		t.Errorf("phase at t=10000ms: got %v, want animPhaseWordmarkFade (exact deadline)", a.phase)
	}
	if a.WordmarkFadeAlpha() != 1.0 {
		t.Errorf("WordmarkFadeAlpha at t=10000ms: got %v, want 1.0 (fade just starting)", a.WordmarkFadeAlpha())
	}

	// Midpoint: easeInOutQuad(0.5) = 0.5, so α = 0.5 exactly.
	a.Advance(animTestOrigin.Add(10300 * time.Millisecond))
	if alpha := a.WordmarkFadeAlpha(); math.Abs(alpha-0.5) > 1e-12 {
		t.Errorf("WordmarkFadeAlpha at t=10300ms: got %v, want 0.5", alpha)
	}

	// One millisecond before the end there is still a trace of wordmark.
	a.Advance(animTestOrigin.Add(10599 * time.Millisecond))
	if alpha := a.WordmarkFadeAlpha(); alpha <= 0 {
		t.Errorf("WordmarkFadeAlpha at t=10599ms: got %v, want > 0", alpha)
	}

	// At exactly 10.60s everything latches: α = 0, dismissed, steady,
	// done.
	a.Advance(animTestOrigin.Add(10600 * time.Millisecond))
	if a.WordmarkFadeAlpha() != 0.0 {
		t.Errorf("WordmarkFadeAlpha at t=10600ms: got %v, want 0.0", a.WordmarkFadeAlpha())
	}
	if !a.wordmarkDismissed {
		t.Error("wordmarkDismissed not latched at t=10600ms")
	}
	if a.phase != animPhaseSteady {
		t.Errorf("phase at t=10600ms: got %v, want animPhaseSteady", a.phase)
	}
	if !a.Done() {
		t.Error("Done not true at t=10600ms")
	}
}

// TestWordmarkFadeUnderSlowEventProcessing simulates a stalled event
// loop that only processes ticks at 0s, 2.0s, 10.2s, and 11.0s (PLAN
// §5.1): the very first frame past the deadline must land on the exact
// fade alpha for its time, with no drift from the missed frames.
func TestWordmarkFadeUnderSlowEventProcessing(t *testing.T) {
	a := newOpeningAnimForTest()

	a.Advance(animTestOrigin)
	a.Advance(animTestOrigin.Add(2 * time.Second))
	if a.WordmarkFadeAlpha() != 1.0 {
		t.Errorf("WordmarkFadeAlpha at t=2.0s: got %v, want 1.0", a.WordmarkFadeAlpha())
	}
	if a.phase != animPhaseLiveWithWordmark {
		t.Errorf("phase at t=2.0s: got %v, want animPhaseLiveWithWordmark", a.phase)
	}

	// The first frame processed after the 10.00s deadline is at 10.2s:
	// it must render the alpha belonging to 10.2s, not restart any
	// timing. (10.2s−10.0s)/0.6s = 1/3 → α = 1 − 2·(1/3)² = 7/9.
	a.Advance(animTestOrigin.Add(10200 * time.Millisecond))
	if a.phase != animPhaseWordmarkFade {
		t.Errorf("phase at t=10.2s: got %v, want animPhaseWordmarkFade", a.phase)
	}
	if alpha := a.WordmarkFadeAlpha(); math.Abs(alpha-7.0/9.0) > 1e-12 {
		t.Errorf("WordmarkFadeAlpha at t=10.2s: got %v, want 7/9 (fade advanced by wall clock)", alpha)
	}

	// Done once and permanently at the next processed frame past 10.6s.
	a.Advance(animTestOrigin.Add(11 * time.Second))
	if a.WordmarkFadeAlpha() != 0.0 {
		t.Errorf("WordmarkFadeAlpha at t=11.0s: got %v, want 0.0", a.WordmarkFadeAlpha())
	}
	if !a.wordmarkDismissed || a.phase != animPhaseSteady || !a.Done() {
		t.Errorf("not fully latched at t=11.0s: dismissed=%v phase=%v done=%v",
			a.wordmarkDismissed, a.phase, a.Done())
	}
}

// TestWordmarkNeverReappearsAfterDismissal covers the Phase-1 slice of
// the PLAN §5.3 invariant: once dismissed, nothing in the animation
// state itself (re-Start from re-init, stale ticks, far-future frames)
// can resurrect the wordmark. The model-level slice (View() after
// /clear, /new, /sessions, resize, new runs) is asserted once the model
// wiring lands in Phase 4/5.
func TestWordmarkNeverReappearsAfterDismissal(t *testing.T) {
	a := newOpeningAnimForTest()
	a.Advance(animTestOrigin.Add(10600 * time.Millisecond))
	if !a.Done() {
		t.Fatalf("precondition: animation not done at t=10.6s")
	}

	// Every TUI reset path funnels into Start/Advance with new
	// timestamps; none may clear the dismissal or move the origin.
	a.Start(animTestOrigin.Add(10600 * time.Millisecond))
	a.Advance(animTestOrigin.Add(10600 * time.Millisecond)) // stale tick
	a.Advance(animTestOrigin.Add(2 * time.Hour))
	if !a.wordmarkDismissed || a.phase != animPhaseSteady || a.WordmarkFadeAlpha() != 0 {
		t.Errorf("resurrection detected: dismissed=%v phase=%v alpha=%v",
			a.wordmarkDismissed, a.phase, a.WordmarkFadeAlpha())
	}

	// Start must also be powerless while the animation is live: a
	// second call cannot shift the origin forward in time.
	early := newOpeningAnimForTest()
	early.Advance(animTestOrigin.Add(500 * time.Millisecond))
	early.Start(animTestOrigin.Add(10 * time.Second))
	early.Advance(animTestOrigin.Add(10 * time.Second))
	if early.phase != animPhaseWordmarkFade {
		t.Errorf("phase after ignored re-Start: got %v, want animPhaseWordmarkFade (origin latched once)", early.phase)
	}
}

// TestWordmarkGradientAndFadeAcrossColorProfiles exercises the gradient
// wave and the three fade renderings (PLAN §5.2) under each color
// profile, including NO_COLOR-style plain output through the Ascii
// profile.
func TestWordmarkGradientAndFadeAcrossColorProfiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    termenv.Profile
	}{
		{"TrueColor", termenv.TrueColor},
		{"ANSI256", termenv.ANSI256},
		{"ANSI", termenv.ANSI},
		{"Ascii", termenv.Ascii},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withOpeningProfile(t, tc.p)
			a := newOpeningAnimForTest()

			switch tc.p {
			case termenv.TrueColor:
				testGradientWaveTrueColor(t, a)
				testFadeTrueColor(t)
			case termenv.ANSI256:
				testRampANSI256(t, a)
			case termenv.ANSI:
				testDissolveColored(t, a)
			case termenv.Ascii:
				testDissolvePlain(t)
			}
		})
	}
}

// testGradientWaveTrueColor checks the gradient phase formula, the ramp
// anchors, seamless wrapping, and that the wave visibly moves between
// t=1.0s and t=1.5s.
func testGradientWaveTrueColor(t *testing.T, a *openingAnimState) {
	t.Helper()

	// φ = mod(0.08x + 0.25y − 1.8Δt, 1): reference values.
	for _, tc := range []struct {
		x, y int
		dt   time.Duration
		want float64
	}{
		{0, 0, time.Second, 0.2},             // mod(−1.8, 1)
		{0, 0, 1500 * time.Millisecond, 0.3}, // mod(−2.7, 1)
		{10, 1, 0, 0.05},                     // 0.8 + 0.25 → mod 1.05
	} {
		if got := wordmarkGradientPhase(tc.x, tc.y, tc.dt); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("wordmarkGradientPhase(%d,%d,%v): got %v, want %v", tc.x, tc.y, tc.dt, got, tc.want)
		}
	}

	// Ramp anchors hit the four named stops exactly.
	for _, tc := range []struct {
		phi  float64
		want string
	}{
		{0, "#A81343"},
		{0.25, "#ED2663"},
		{0.5, "#F4729A"},
		{0.75, "#FFB3C9"},
	} {
		if got := wordmarkTrueColor(tc.phi, 1.0); got != lipgloss.Color(tc.want) {
			t.Errorf("wordmarkTrueColor(φ=%v): got %v, want %v (ramp stop)", tc.phi, got, tc.want)
		}
	}
	// Seamless wrap: φ just past the end equals φ just before 0.
	if wordmarkTrueColor(0.9999, 1.0) != wordmarkTrueColor(-0.0001, 1.0) {
		t.Error("gradient wave does not wrap seamlessly (φ=0.9999 vs φ=−0.0001 differ)")
	}

	// Live wave motion: cell (0,0) at t=1.0s (φ=0.2) vs t=1.5s (φ=0.3)
	// sits on different ramp segments, so the two frames differ.
	a.Advance(animTestOrigin.Add(time.Second))
	frame1 := wordmarkTrueColor(wordmarkGradientPhase(0, 0, time.Second), a.WordmarkFadeAlpha())
	a.Advance(animTestOrigin.Add(1500 * time.Millisecond))
	frame2 := wordmarkTrueColor(wordmarkGradientPhase(0, 0, 1500*time.Millisecond), a.WordmarkFadeAlpha())
	if frame1 == frame2 {
		t.Errorf("gradient wave static between t=1.0s and t=1.5s: both %v", frame1)
	}
}

// testFadeTrueColor checks the ⌊α·channel⌋ scaling at fade boundaries.
func testFadeTrueColor(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		phi, alpha float64
		want       string
	}{
		{0.25, 1.0, "#ED2663"}, // α=1 reproduces the wave stop exactly
		{0, 0.5, "#540921"},    // 0.5·#A81343 with floor: 84 9 33
		{0.25, 0.0, "#000000"}, // fully faded
	} {
		if got := wordmarkTrueColor(tc.phi, tc.alpha); got != lipgloss.Color(tc.want) {
			t.Errorf("wordmarkTrueColor(φ=%v, α=%v): got %v, want %v", tc.phi, tc.alpha, got, tc.want)
		}
	}
}

// testRampANSI256 checks the seven-step fade ramp boundaries of
// PLAN §5.2.2, including bucket-edge membership.
func testRampANSI256(t *testing.T, a *openingAnimState) {
	t.Helper()
	for _, tc := range []struct {
		alpha float64
		want  string
	}{
		{1.0, "204"}, {0.85, "204"}, {0.849, "197"},
		{0.70, "197"}, {0.55, "161"}, {0.40, "125"},
		{0.25, "88"}, {0.10, "52"}, {0.05, "235"},
	} {
		if got := wordmarkANSI256(tc.alpha); string(got) != tc.want {
			t.Errorf("wordmarkANSI256(α=%v): got %q, want %q", tc.alpha, got, tc.want)
		}
	}
	if got := wordmarkANSI256(0); got != lipgloss.Color("") {
		t.Errorf("wordmarkANSI256(α=0): got %q, want the unmounted sentinel", got)
	}

	// The state's alpha drives the ramp: live phase → top rung,
	// mid-fade at 10300ms → 125.
	a.Advance(animTestOrigin.Add(time.Second))
	if got := wordmarkANSI256(a.WordmarkFadeAlpha()); string(got) != "204" {
		t.Errorf("ramp at t=1.0s: got %q, want 204 before the fade", got)
	}
	a.Advance(animTestOrigin.Add(10300 * time.Millisecond))
	if got := wordmarkANSI256(a.WordmarkFadeAlpha()); string(got) != "125" {
		t.Errorf("ramp at t=10.3s: got %q, want 125 (α=0.5)", got)
	}
}

// testDissolveColored checks the glyph-density dissolve of PLAN §5.2.3
// on a color-capable ANSI16 terminal: colors survive and every half
// block skips the heavier ▓ tier.
func testDissolveColored(t *testing.T, a *openingAnimState) {
	t.Helper()
	for _, tc := range []struct {
		glyph rune
		alpha float64
		want  rune
	}{
		{'█', 1.0, '█'}, {'█', 0.75, '█'}, {'█', 0.74, '▓'},
		{'█', 0.50, '▓'}, {'█', 0.49, '░'}, {'█', 0.25, '░'}, {'█', 0.24, '·'},
		{'▀', 0.80, '▀'}, {'▀', 0.74, '░'}, {'▄', 0.45, '·'},
		{'·', 0.9, '·'}, // non-block runes pass through unchanged
	} {
		if got := wordmarkFadeGlyph(tc.glyph, tc.alpha); got != tc.want {
			t.Errorf("wordmarkFadeGlyph(%q, α=%v): got %q, want %q", tc.glyph, tc.alpha, got, tc.want)
		}
		if !strings.Contains(wordmarkFadeStyle(tc.alpha).Render("█"), "\x1b[") {
			t.Errorf("style at α=%v lost its ANSI styling on a color terminal", tc.alpha)
		}
	}

	// Full-block tier differences: bold vs plain vs faint each render
	// distinct escape sequences.
	bold := wordmarkFadeStyle(1.0).Render("█")
	plain := wordmarkFadeStyle(0.6).Render("█")
	faint := wordmarkFadeStyle(0.3).Render("█")
	if bold == plain || plain == faint || bold == faint {
		t.Error("dissolve tiers render identically; weight ladder collapsed")
	}

	// State-driven: α=1 renders the steady bold wordmark look.
	a.Advance(animTestOrigin.Add(time.Second))
	if got := wordmarkFadeStyle(a.WordmarkFadeAlpha()); !strings.Contains(got.Render("█"), "\x1b[") {
		t.Error("steady-state wordmark lost its bold magenta styling")
	}
}

// testDissolvePlain runs the dissolve on an Ascii-profile terminal
// (NO_COLOR, FORCEFIELD_ASCII, pipe): the typography still dissolves,
// but styles render as plain glyphs with no escape sequences.
func testDissolvePlain(t *testing.T) {
	t.Helper()
	if got := wordmarkFadeStyle(1.0).Render("█"); got != "█" {
		t.Errorf("Ascii profile styled the wordmark: got %q, want plain \"█\"", got)
	}
	if got := wordmarkFadeGlyph('█', 0.1); got != '·' {
		t.Errorf("Ascii profile lost the dissolve: got %q, want '·'", got)
	}
	if got := wordmarkTrueColor(0, 1.0); !strings.HasPrefix(string(got), "#") {
		t.Errorf("TrueColor math must stay plain text on any profile: got %q", got)
	}
}
