package tui

import (
	"strings"
	"testing"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

func TestFormatToolStartIncludesUsefulArguments(t *testing.T) {
	got := formatToolStart(&providers.ToolCall{
		Name:      "read_file",
		Arguments: map[string]any{"path": "README.md"},
	})
	if got != "● Running read_file README.md" {
		t.Errorf("formatToolStart() = %q", got)
	}
}

func TestFormatToolFinishSummarizesListFiles(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:     "list_files",
		Content:  "README.md\ncmd/\ninternal/",
		Success:  true,
		Duration: 150 * time.Millisecond,
	}, runtime.EventToolFinish)
	if got != "✳ Found 3 entries (150ms)" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishSearchUsesStar(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:     "search_files",
		Content:  "a.go:1: foo\nb.go:2: foo bar",
		Success:  true,
		Duration: 210 * time.Millisecond,
	}, runtime.EventToolFinish)
	if got != "✳ Found 2 matches (210ms)" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishSearchNoMatchesFallsBack(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:    "search_files",
		Content: "no matches for \"zzz\" under .",
		Success: true,
	}, runtime.EventToolFinish)
	if got != "◈ search_files completed" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishReadUsesDiamond(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:      "read_file",
		Arguments: map[string]any{"path": "go.mod"},
		Content:   "module foo",
		Success:   true,
	}, runtime.EventToolFinish)
	if got != "◈ Read go.mod" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishGenericUsesDiamond(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:    "shell",
		Content: "ok",
		Success: true,
	}, runtime.EventToolFinish)
	if got != "◈ shell completed" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishDoesNotExposeLargeFailureOutput(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:    "read_file",
		Content: strings.Repeat("x", 200),
		Success: false,
	}, runtime.EventToolFailed)
	if len(got) > 160 || !strings.HasPrefix(got, "✕ read_file failed: ") {
		t.Errorf("formatToolFinish() = %q", got)
	}
}
