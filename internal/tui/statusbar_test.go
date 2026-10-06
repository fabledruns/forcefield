package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// stripANSI is defined in markdown_reliability_test.go.

func TestStatusBarFullWidth(t *testing.T) {
	for _, kind := range []statusKind{statusSuccess, statusInfo, statusUpdate, statusWarn, statusError} {
		got := renderStatusBar(80, kind, "hello")
		if w := lipgloss.Width(got); w != 80 {
			t.Errorf("kind %d width = %d, want 80", kind, w)
		}
		if h := lipgloss.Height(got); h != 1 {
			t.Errorf("kind %d height = %d, want 1 (same as Crush)", kind, h)
		}
		plain := stripANSI(got)
		if strings.Contains(plain, "\n") {
			t.Errorf("kind %d contains newline, want single row", kind)
		}
	}
}

func TestStatusBarBadges(t *testing.T) {
	cases := []struct {
		kind  statusKind
		badge string
	}{
		{statusSuccess, "OKAY!"},
		{statusInfo, "OKAY!"},
		{statusUpdate, "HEY!"},
		{statusWarn, "WARNING"},
		{statusError, "ERROR"},
	}
	for _, tc := range cases {
		got := stripANSI(renderStatusBar(80, tc.kind, "msg"))
		if !strings.HasPrefix(got, " "+tc.badge+" ") && !strings.Contains(got, tc.badge) {
			t.Errorf("kind %d = %q, want badge %q", tc.kind, got, tc.badge)
		}
		// Badge sits on the far left (after left padding), message follows.
		if idx := strings.Index(got, tc.badge); idx > 2 {
			t.Errorf("kind %d badge at %d, want far left (Crush proportions)", tc.kind, idx)
		}
	}
}

func TestStatusBarTruncatesAndFills(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := renderStatusBar(40, statusError, long)
	if w := lipgloss.Width(got); w != 40 {
		t.Fatalf("truncated width = %d, want 40", w)
	}
	plain := stripANSI(got)
	if !strings.Contains(plain, "…") {
		t.Errorf("long message not truncated with ellipsis: %q", plain)
	}

	short := renderStatusBar(40, statusSuccess, "hi")
	if w := lipgloss.Width(short); w != 40 {
		t.Errorf("short width = %d, want full-width solid strip 40", w)
	}

	multi := stripANSI(renderStatusBar(80, statusWarn, "a\nb\nc"))
	if strings.Contains(multi, "\n") {
		t.Errorf("newlines must collapse to spaces (Crush Draw): %q", multi)
	}
	if !strings.Contains(multi, "a b c") {
		t.Errorf("newline collapse = %q, want %q", multi, "a b c")
	}
}

func TestStatusBarNoChrome(t *testing.T) {
	// No icons, borders, or rounding: only badge text + message text.
	got := stripANSI(renderStatusBar(80, statusError, "boom"))
	for _, bad := range []string{"│", "─", "╭", "╰", "●", "■", "✓", "✕"} {
		if strings.Contains(got, bad) {
			t.Errorf("status bar must not contain %q: %q", bad, got)
		}
	}
}

func TestFooterShowsStatusBarOverHelp(t *testing.T) {
	m := newTestModel()
	m.width = 80
	m.height = 24
	m.layout()
	m.setNotice(statusUpdate, "Crush update available: v0.96.1 → v0.97.1.")
	footer := stripANSI(m.renderFooter())
	if !strings.Contains(footer, "HEY!") {
		t.Fatalf("footer missing HEY! badge: %q", footer)
	}
	if !strings.Contains(footer, "Crush update available") {
		t.Fatalf("footer missing message: %q", footer)
	}
	// Same height: notice replaces the help line, it must not add rows.
	m.clearNotice()
	before := m.footerHeight()
	m.setNotice(statusError, "boom")
	after := m.footerHeight()
	if before != after {
		t.Errorf("footerHeight with notice = %d, want %d (same row)", after, before)
	}
}

func TestNoticeColorsDiffer(t *testing.T) {
	ok := renderStatusBar(80, statusSuccess, "ok")
	warn := renderStatusBar(80, statusWarn, "ok")
	errBar := renderStatusBar(80, statusError, "ok")
	if ok == warn || ok == errBar || warn == errBar {
		t.Error("green/yellow/red strips must differ (no Forcefield substitution)")
	}
}
