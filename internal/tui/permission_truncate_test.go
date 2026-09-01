package tui

import (
	"strings"
	"testing"

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
