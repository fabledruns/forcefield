package tui

import (
	"errors"
	"strings"
	"testing"
)

// emojiPresentationBlocklist holds code points that render as color
// emoji (rather than text) on at least one supported terminal,
// notably through Windows font fallback. No internal UI glyph may
// contain them; model message rendering is a separate path and is
// unaffected by this test.
var emojiPresentationBlocklist = []rune{
	0x2699, // gear (was IconSettings)
	0x2733, // eight-spoked asterisk (was IconStar8)
	0x26A0, // warning sign (was discovery picker status)
	0x2B50, // star
	0x2705, // check mark button
	0x274C, // cross mark button
	0x203C, // double exclamation
	0x2049, // question exclamation
	0xFE0F, // variation selector-16 (forces emoji presentation)
	0x20E3, // combining enclosing keycap
}

func isBlocklisted(r rune) bool {
	for _, b := range emojiPresentationBlocklist {
		if r == b {
			return true
		}
	}
	// Emoticons, pictographs, transport/map symbols, and regional
	// indicators are emoji-presentation territory throughout.
	if r >= 0x1F300 && r <= 0x1FAFF {
		return true
	}
	if r >= 0x1F1E6 && r <= 0x1F1FF {
		return true
	}
	return false
}

// TestIconsHaveTextPresentation pins issue #13: every glyph in the
// closed Icon set must have text presentation so Windows terminals
// never substitute emoji. Adding an Icon constant requires adding it
// to the list below (it is compile-checked).
func TestIconsHaveTextPresentation(t *testing.T) {
	icons := map[string]Icon{
		"Prompt":    IconPrompt,
		"Collapsed": IconCollapsed,
		"Expanded":  IconExpanded,
		"Success":   IconSuccess,
		"Failure":   IconFailure,
		"Diamond":   IconDiamond,
		"Star8":     IconStar8,
		"Thinking":  IconThinking,
		"Warning":   IconWarning,
		"Cancel":    IconCancel,
		"Running":   IconRunning,
		"Idle":      IconIdle,
		"Think":     IconThink,
		"Pipe":      IconPipe,
		"Sep":       IconSep,
		"Ellipsis":  IconEllipsis,
		"Shell":     IconShell,
		"File":      IconFile,
		"Git":       IconGit,
		"Search":    IconSearch,
		"Settings":  IconSettings,
		"Memory":    IconMemory,
		"Model":     IconModel,
		"Session":   IconSession,
		"Skill":     IconSkill,
		"Tool":      IconTool,
	}
	if len(icons) != 26 {
		t.Fatalf("icon list has %d entries, keep it in sync with the Icon constants", len(icons))
	}
	for name, icon := range icons {
		for _, r := range string(icon) {
			if isBlocklisted(r) {
				t.Errorf("Icon%s %q contains emoji-presentation rune U+%04X", name, string(icon), r)
			}
		}
	}
}

// TestCompactErrorHasTextPresentation pins the same rule for the
// discovery picker status line, which builds its warning glyph outside
// the Icon set.
func TestCompactErrorHasTextPresentation(t *testing.T) {
	out := compactError(errors.New("something failed\nwith newline"))
	if !strings.HasPrefix(out, "! ") {
		t.Errorf("compactError = %q, want text warning marker", out)
	}
	for _, r := range out {
		if isBlocklisted(r) {
			t.Errorf("compactError %q contains emoji-presentation rune U+%04X", out, r)
		}
	}
}
