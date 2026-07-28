package tui

import (
	"strings"

	"charm.land/glamour/v2"
)

// renderMarkdown renders complete Markdown while a response is streaming.
// The unfinished tail remains plain text until it is structurally complete,
// which avoids flickering partial emphasis and malformed code blocks.
func renderMarkdown(markdown string, width int, streaming bool) string {
	stable, tail := completeMarkdown(markdown, streaming)
	rendered := renderCompleteMarkdown(stable, width)
	if tail == "" {
		return rendered
	}

	tail = messageBodyStyle.Width(width).Render(tail)
	if rendered == "" {
		return tail
	}
	return strings.TrimRight(rendered, "\n") + "\n" + tail
}

func renderCompleteMarkdown(markdown string, width int) string {
	if markdown == "" {
		return ""
	}
	if width < 20 {
		width = 20
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return messageBodyStyle.Width(width).Render(markdown)
	}
	rendered, err := renderer.Render(markdown)
	if err != nil {
		return messageBodyStyle.Width(width).Render(markdown)
	}
	return strings.TrimRight(rendered, "\n")
}

func completeMarkdown(markdown string, streaming bool) (stable, tail string) {
	if !streaming {
		return markdown, ""
	}

	lastNewline := strings.LastIndex(markdown, "\n")
	if lastNewline < 0 {
		return "", markdown
	}

	stable = markdown[:lastNewline+1]
	tail = markdown[lastNewline+1:]
	lines := strings.SplitAfter(stable, "\n")
	fenceStart := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if fenceStart < 0 {
				fenceStart = i
			} else {
				fenceStart = -1
			}
		}
	}
	if fenceStart < 0 {
		return stable, tail
	}

	prefix := strings.Join(lines[:fenceStart], "")
	return prefix, strings.Join(lines[fenceStart:], "") + tail
}
