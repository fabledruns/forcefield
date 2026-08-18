package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	bodyIndent      = 2
	toolNameWidth   = 16
	toolStatusWidth = 10
	minModalWidth   = 20
)

// truncateCells shortens s to at most width display cells, appending an
// ellipsis when it had to cut. Width is measured the same way Lip Gloss
// measures it, so wide runes are not split.
func truncateCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	ellipsis := IconEllipsis.String()
	ew := lipgloss.Width(ellipsis)
	if width <= ew {
		return ellipsis
	}

	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw+ew > width {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteString(ellipsis)
	return b.String()
}

// indentBlock prefixes every line of s. ANSI sequences in s are left
// intact; the prefix is inserted at the start of each line.
func indentBlock(s, prefix string) string {
	if s == "" || prefix == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func indentPrefix() string {
	return strings.Repeat(" ", bodyIndent)
}

func innerWidth(width int) int {
	if width <= bodyIndent+1 {
		return max(width, 1)
	}
	return width - bodyIndent
}

func modalWidth(termWidth int) int {
	w := pickerWidth
	if termWidth > 0 && termWidth-4 < w {
		w = termWidth - 4
	}
	if w < minModalWidth {
		w = minModalWidth
	}
	return w
}

func fitWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(truncateCells(s, width))
}
