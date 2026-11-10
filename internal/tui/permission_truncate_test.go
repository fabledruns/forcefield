package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"forcefield/internal/permissions"
)

func TestPermissionPrompt_TruncationIsExplicit(t *testing.T) {
	longContent := strings.Repeat("A", 500)
	req := permissions.Request{
		Tool: "write_file",
		Arguments: map[string]any{
			"path":    "file.txt",
			"content": longContent,
		},
	}
	prompt := &permissionPrompt{request: req, selected: 0}
	block := prompt.formatToolBlock()
	if !strings.Contains(block, "truncated") {
		t.Errorf("permission prompt should explicitly note truncation for long content, got %q", block)
	}
	if strings.Contains(block, longContent) {
		t.Errorf("permission prompt should not contain full long content")
	}
	// Ensure it shows the byte count
	if !strings.Contains(block, "500") {
		t.Errorf("should show total bytes, got %q", block)
	}
}

func TestPermissionPrompt_MultilineShowsHiddenCount(t *testing.T) {
	content := "line1\nline2\nline3\nline4"
	req := permissions.Request{
		Tool: "write_file",
		Arguments: map[string]any{
			"path":    "file.txt",
			"content": content,
		},
	}
	prompt := &permissionPrompt{request: req, selected: 0}
	block := prompt.formatToolBlock()
	if !strings.Contains(block, "hidden") {
		t.Errorf("multiline should note hidden lines, got %q", block)
	}
}

// --- byte-limit truncation ----------------------------------------------------------
//
// The permission prompt caps argument previews by byte count. A plain
// byte cut can land inside a multi-byte character and render mojibake,
// so the cut is aligned back to a character boundary while the byte
// budget itself is preserved.

func TestTruncateBytesShortStringUnchanged(t *testing.T) {
	if got := truncateBytes("short", 80); got != "short" {
		t.Errorf("truncateBytes = %q, want unchanged", got)
	}
}

func TestTruncateBytesEmpty(t *testing.T) {
	if got := truncateBytes("", 300); got != "" {
		t.Errorf("truncateBytes(\"\") = %q, want empty", got)
	}
}

func TestTruncateBytesASCIIAtLimitUnchanged(t *testing.T) {
	value := strings.Repeat("a", 300)
	if got := truncateBytes(value, 300); got != value {
		t.Errorf("truncateBytes cut an ASCII string at exactly the limit")
	}
}

func TestTruncateBytesKeepsCharacterBoundary(t *testing.T) {
	value := strings.Repeat("世", 10) // 10 characters, 30 bytes
	got := truncateBytes(value, 8)   // 8 bytes would split the third character
	if want := strings.Repeat("世", 2); got != want {
		t.Errorf("truncateBytes = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncateBytes produced invalid UTF-8: %q", got)
	}
}

func TestTruncateBytesWithinBudget(t *testing.T) {
	value := "a" + strings.Repeat("世", 200) // 601 bytes
	got := truncateBytes(value, 300)
	if len(got) > 300 {
		t.Errorf("truncateBytes returned %d bytes, want at most 300", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncateBytes produced invalid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("truncateBytes lost bytes mid-character: %q", got)
	}
}

// TestPermissionPromptMultibyteTruncationStaysValidUTF8 checks the
// rendered permission block end to end: long multi-byte argument
// content must stay valid UTF-8 and keep the explicit truncation note.
func TestPermissionPromptMultibyteTruncationStaysValidUTF8(t *testing.T) {
	content := "a" + strings.Repeat("世", 200) // 601 bytes, exceeds the 300-byte cap
	req := permissions.Request{
		Tool: "write_file",
		Arguments: map[string]any{
			"path":    "file.txt",
			"content": content,
		},
	}
	prompt := &permissionPrompt{request: req, selected: 0}
	block := prompt.formatToolBlock()
	if !utf8.ValidString(block) {
		t.Errorf("permission block is not valid UTF-8")
	}
	if strings.ContainsRune(block, utf8.RuneError) {
		t.Errorf("permission block contains a replacement character")
	}
	if !strings.Contains(block, "truncated") {
		t.Errorf("permission block should note truncation explicitly, got %q", block)
	}
	if !strings.Contains(block, "601 bytes total") {
		t.Errorf("permission block should report the original byte count, got %q", block)
	}
}
