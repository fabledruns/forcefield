package tui

import (
	"strings"
	"testing"
)

func TestCompleteMarkdownDefersPartialCodeBlock(t *testing.T) {
	stable, tail := completeMarkdown("Before\n```go\nfmt.Println(\"hello\")\n", true)
	if stable != "Before\n" {
		t.Errorf("stable = %q, want only the completed paragraph", stable)
	}
	if !strings.HasPrefix(tail, "```go") {
		t.Errorf("tail = %q, want unfinished code block", tail)
	}
}

func TestCompleteMarkdownRendersFullResponseWhenFinished(t *testing.T) {
	markdown := "# Heading\n\n**Forcefield**\n"
	stable, tail := completeMarkdown(markdown, false)
	if stable != markdown || tail != "" {
		t.Errorf("completeMarkdown() = (%q, %q), want complete response", stable, tail)
	}
}

func TestRenderMarkdownRendersInlineCode(t *testing.T) {
	rendered := renderMarkdown("Run `go test ./...`.", 80, false)
	if !strings.Contains(rendered, "go test ./...") {
		t.Errorf("rendered Markdown did not contain inline code: %q", rendered)
	}
}
