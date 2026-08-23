package runtime

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateToolResultKeepsValidUTF8(t *testing.T) {
	// A string whose maxToolResultChars boundary lands mid-rune: the
	// truncated prefix must still be valid UTF-8, never a broken
	// half-character that would poison later provider requests.
	content := strings.Repeat("é", 100) + strings.Repeat("a", maxToolResultChars)
	got := truncateToolResult(content)

	if !utf8.ValidString(got) {
		t.Fatal("truncated tool result is not valid UTF-8")
	}
	if !strings.Contains(got, "[...output truncated") {
		t.Error("truncation notice missing from over-limit result")
	}
}

func TestTruncateToolResultUnderLimitUnchanged(t *testing.T) {
	content := "short ✓ output"
	if got := truncateToolResult(content); got != content {
		t.Errorf("truncateToolResult(%q) = %q, want unchanged", content, got)
	}
}

func TestTruncateToolResultTrimsOnlyRuneOverflow(t *testing.T) {
	// Pure multi-byte content just under 2x the cap: everything except a
	// small rune-boundary remainder fits.
	content := strings.Repeat("世", (maxToolResultChars/3)+10) // each rune is 3 bytes
	if len(content) <= maxToolResultChars {
		t.Fatalf("test setup: content is only %d bytes", len(content))
	}

	got := truncateToolResult(content)
	if !utf8.ValidString(got) {
		t.Fatal("truncated result is not valid UTF-8")
	}
	if !strings.Contains(got, "[...output truncated at") {
		t.Errorf("got %q, want truncation notice", got[:min(len(got), 80)])
	}
}
