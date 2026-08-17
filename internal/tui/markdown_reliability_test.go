package tui

import (
	"strings"
	"testing"
)

const asciiFlowchart = "  +--------+     +--------+\n  | Client | --> | Server |\n  +--------+     +--------+"

func TestPreserveAsciiDiagramsFencesFlowchart(t *testing.T) {
	out := preserveAsciiDiagrams(asciiFlowchart)
	if !strings.Contains(out, "```\n  +--------+") {
		t.Errorf("flowchart not fenced:\n%s", out)
	}
	if strings.Count(out, "```") != 2 {
		t.Errorf("want exactly one fence pair, got:\n%s", out)
	}
}

func TestPreserveAsciiDiagramsLeavesMarkdownTable(t *testing.T) {
	table := "| Name | Value |\n|---|---|\n| a | 1 |\n| b | 2 |"
	out := preserveAsciiDiagrams(table)
	if strings.Contains(out, "```") {
		t.Errorf("markdown table was fenced as a diagram:\n%s", out)
	}
}

func TestPreserveAsciiDiagramsLeavesProseWithSlashes(t *testing.T) {
	prose := "Edit C:\\Users\\me\\file.txt and/or its sibling.\nAlso see https://example.com/a/b."
	out := preserveAsciiDiagrams(prose)
	if strings.Contains(out, "```") {
		t.Errorf("prose with slashes was fenced as a diagram:\n%s", out)
	}
}

func TestRenderMarkdownPreservesAsciiFlowchartLayout(t *testing.T) {
	rendered := renderMarkdown(asciiFlowchart, 80, false)
	for _, line := range strings.Split(asciiFlowchart, "\n") {
		if !strings.Contains(rendered, strings.TrimRight(line, " ")) {
			t.Errorf("rendered output lost flowchart line %q:\n%s", line, rendered)
		}
	}
}

func TestRenderMarkdownNoBlockBackgrounds(t *testing.T) {
	// Headings and code in the stock dark style carry background colors
	// that clash with the red-on-black palette behind ASCII art.
	rendered := renderMarkdown("# Title\n\n"+asciiFlowchart+"\n\nsome `code`", 80, false)
	if strings.Contains(rendered, "\x1b[48;") {
		t.Errorf("rendered Markdown contains background-color SGR sequences:\n%q", rendered)
	}
}

func TestRenderMarkdownNarrowWidthDoesNotOverflow(t *testing.T) {
	// A terminal narrower than glamour's comfortable minimum must still
	// render without forcing a 20-column wrap that overflows.
	rendered := renderMarkdown("word "+strings.Repeat("verylongword ", 8), 10, false)
	for _, line := range strings.Split(rendered, "\n") {
		if len([]rune(stripANSI(line))) > 20 {
			t.Errorf("line wider than the terminal:\n%q", line)
		}
	}
}

// stripANSI removes SGR/CSI escape sequences so widths can be measured.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
