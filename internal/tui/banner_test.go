package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func strippedBannerLines(t *testing.T, width int) []string {
	t.Helper()
	return strings.Split(stripANSI(renderBanner(width)), "\n")
}

func TestBannerLeftAlignedWithRail(t *testing.T) {
	lines := strippedBannerLines(t, 100)
	if len(lines) < 5 {
		t.Fatalf("banner has %d lines, want margin + art + rail + tagline", len(lines))
	}
	if strings.TrimSpace(lines[0]) != "" {
		t.Errorf("line 0 = %q, want blank top margin", lines[0])
	}
	for _, idx := range []int{1, 2, 3} {
		if !strings.HasPrefix(lines[idx], "|  ") {
			t.Errorf("art row %d = %q, want %q prefix", idx-1, lines[idx], "|  ")
		}
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, "|") {
			t.Errorf("line %d = %q, want left-aligned rail starting with %q", i, l, "|")
		}
		if strings.HasPrefix(l, " ") {
			t.Errorf("line %d = %q, want no leading space before rail", i, l)
		}
	}
}

func TestBannerHasNoSlashes(t *testing.T) {
	got := stripANSI(renderBanner(100))
	if strings.Contains(got, "|/") {
		t.Errorf("banner still has forward-slash gutter:\n%s", got)
	}
	if strings.Contains(got, `|\`) {
		t.Errorf(`banner still has backslash gutter:\n%s`, got)
	}
}

func TestBannerTopMargin(t *testing.T) {
	for _, width := range []int{40, 100} {
		lines := strippedBannerLines(t, width)
		if len(lines) == 0 || strings.TrimSpace(lines[0]) != "" {
			t.Errorf("width %d first line = %q, want blank top margin", width, lines[0])
		}
	}
}

func TestBannerTaglineRenamed(t *testing.T) {
	const want = "an ultra-fast, lightweight, local-first AI agent harness for running specialized agents"
	for _, width := range []int{100, 120} {
		got := stripANSI(renderBanner(width))
		flat := strings.Join(strings.Split(got, "\n"), " ")
		flat = strings.Join(strings.Fields(flat), " ")
		if !strings.Contains(flat, want) {
			t.Errorf("width %d banner missing new tagline:\n%s", width, got)
		}
		if strings.Contains(flat, "the local-first agent harness") {
			t.Errorf("width %d banner still carries old tagline:\n%s", width, got)
		}
	}
}

func TestBannerTaglineWrapsNarrow(t *testing.T) {
	lines := strippedBannerLines(t, 50)
	tagLines := 0
	for _, l := range lines[3:] {
		if strings.TrimSpace(strings.TrimPrefix(l, "|")) != "" {
			tagLines++
		}
		if l != "" && !strings.HasPrefix(l, "|") {
			t.Errorf("wrapped line = %q, want rail prefix", l)
		}
	}
	if tagLines < 2 {
		t.Errorf("width 50 tagline takes %d lines, want wrapped (>=2):\n%s", tagLines, strings.Join(lines, "\n"))
	}
}

func TestBannerGradientEndpoints(t *testing.T) {
	if got := string(bannerGradientColor(0)); got != "#ED2663" {
		t.Errorf("t=0 = %q, want Forcefield pink", got)
	}
	if got := string(bannerGradientColor(1)); got != "#F2F2F2" {
		t.Errorf("t=1 = %q, want off-white", got)
	}
	if got := string(bannerGradientColor(0.5)); got != "#F08CAB" {
		t.Errorf("t=0.5 = %q, want midpoint", got)
	}
	if got := string(bannerGradientColor(-1)); got != "#ED2663" {
		t.Errorf("t=-1 = %q, want clamped pink", got)
	}
	if got := string(bannerGradientColor(2)); got != "#F2F2F2" {
		t.Errorf("t=2 = %q, want clamped off-white", got)
	}
}

func TestBannerGradientPreservesRunes(t *testing.T) {
	for _, line := range nonEmptyArtLines(asciiBanner) {
		if got := stripANSI(renderGradientLine(line)); got != line {
			t.Errorf("gradient altered art runes:\n got %q\nwant %q", got, line)
		}
	}
	if got := stripANSI(renderGradientLine("FORCEFIELD")); got != "FORCEFIELD" {
		t.Errorf("compact gradient altered title: %q", got)
	}
	if got := stripANSI(renderGradientLine("")); got != "" {
		t.Errorf("empty line = %q, want empty", got)
	}
}

func TestBannerArtUnchangedByGradient(t *testing.T) {
	lines := strippedBannerLines(t, 100)
	art := nonEmptyArtLines(asciiBanner)
	for i := 0; i < 3; i++ {
		row := strings.TrimRight(strings.TrimPrefix(lines[1+i], "|  "), " ")
		if want := strings.TrimRight(art[i], " "); row != want {
			t.Errorf("art row %d changed:\n got %q\nwant %q", i, row, want)
		}
	}
}

func TestBannerVersionLabel(t *testing.T) {
	prev := Version
	defer func() { Version = prev }()
	for _, tc := range []struct{ in, want string }{
		{"dev", "dev"},
		{"", "dev"},
		{"  ", "dev"},
		{"v1.2.3", "v1.2.3"},
		{"1.2.3", "v1.2.3"},
	} {
		Version = tc.in
		if got := bannerVersionLabel(); got != tc.want {
			t.Errorf("Version %q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBannerVersionRow(t *testing.T) {
	lines := strippedBannerLines(t, 100)
	if len(lines) < 6 {
		t.Fatalf("banner has %d lines, want margin + art + version + rail + tagline", len(lines))
	}
	// Art rows untouched, version directly beneath on the same rail.
	for _, idx := range []int{1, 2, 3} {
		if !strings.HasPrefix(lines[idx], "|  ") {
			t.Errorf("art row %d = %q, want rail prefix", idx-1, lines[idx])
		}
	}
	if want := "|  " + bannerVersionLabel(); strings.TrimRight(lines[4], " ") != want {
		t.Errorf("version row = %q, want %q", lines[4], want)
	}
	if strings.TrimRight(lines[5], " ") != "|" {
		t.Errorf("line 5 = %q, want bare rail separator", lines[5])
	}
	if got := stripANSI(renderVersionLine(bannerVersionLabel())); got != bannerVersionLabel() {
		t.Errorf("version gradient altered label: %q", got)
	}
}

func TestBannerVersionGradient(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	rows := strings.Split(renderBanner(100), "\n")
	const pink, white = "237;38;99", "242;242;242" // #ED2663, #F2F2F2
	if !strings.Contains(rows[4], pink) || !strings.Contains(rows[4], white) {
		t.Errorf("version row missing pink→off-white gradient:\n%s", rows[4])
	}
}

func TestCompactBannerVersion(t *testing.T) {
	lines := strippedBannerLines(t, 40)
	if len(lines) < 4 {
		t.Fatalf("compact banner has %d lines, want margin + title + version + rail", len(lines))
	}
	if want := "|  " + bannerVersionLabel(); strings.TrimRight(lines[2], " ") != want {
		t.Errorf("compact version row = %q, want %q", lines[2], want)
	}
}

func TestCompactBannerKeepsRail(t *testing.T) {
	lines := strippedBannerLines(t, 40)
	if strings.TrimSpace(lines[0]) != "" {
		t.Errorf("compact line 0 = %q, want blank top margin", lines[0])
	}
	if !strings.HasPrefix(lines[1], "|  ") {
		t.Errorf("compact row 0 = %q, want rail", lines[1])
	}
	if !strings.Contains(lines[1], "FORCEFIELD") {
		t.Errorf("compact banner lost FORCEFIELD title: %q", lines[1])
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, "|") {
			t.Errorf("compact line = %q, want rail prefix", l)
		}
	}
}
