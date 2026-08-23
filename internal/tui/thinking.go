package tui

import (
	"fmt"
	"strings"
	"time"
)

// thinkingRecord carries one model turn's streamed reasoning for the
// lifetime of its transcript entry, so the compact "Thinking" line can be
// expanded (ctrl+r) into the reasoning the provider sent. Forcefield only
// ever displays reasoning the provider explicitly streamed as reasoning
// deltas - it never generates, summarizes, or reconstructs any - and the
// text is kept out of the assistant message and the saved session.
type thinkingRecord struct {
	text      string
	startedAt time.Time
	endedAt   time.Time // zero while reasoning is still streaming
	expanded  bool
}

func (t *thinkingRecord) streaming() bool { return t.endedAt.IsZero() }

// duration returns the reasoning phase's duration so far (live) or total
// (finished), for the Thinking header.
func (t *thinkingRecord) duration() time.Duration {
	if t.streaming() {
		return time.Since(t.startedAt)
	}
	return t.endedAt.Sub(t.startedAt)
}

// liveThinkingLines is how many trailing reasoning lines stay visible under
// the Thinking header while the block is streaming but not expanded: enough
// to see progress happen, not enough to flood the transcript.
const liveThinkingLines = 3

// renderThinking renders one reasoning block as a collapsible transcript
// entry. While reasoning streams, a short tail of the text stays visible so
// progress is obvious; once the turn moves on it collapses to the header
// line until expanded with ctrl+r. hovered marks the block under the
// pointer for subtle emphasis.
func renderThinking(t *thinkingRecord, width int, hovered bool) string {
	caret := IconCollapsed
	if t.expanded {
		caret = IconExpanded
	}
	header := fmt.Sprintf("%s %s Thinking  %s",
		caret, IconThink, formatThinkingDuration(t.duration()))
	if hovered {
		header = hoverEmphasisStyle.Render(header)
	} else {
		header = thinkStyle.Render(header)
	}

	var lines []string
	if text := strings.TrimRight(t.text, "\n"); text != "" {
		if t.expanded {
			lines = clampLines(text, maxExpandedOutputLines)
		} else if t.streaming() {
			lines = tailLines(text, liveThinkingLines)
		}
	}
	if len(lines) == 0 {
		return header
	}

	indent := strings.Repeat(" ", 2)
	for i, line := range lines {
		lines[i] = indent + line
	}
	return header + "\n" + thinkStepStyle.Width(width).Render(strings.Join(lines, "\n"))
}

// formatThinkingDuration renders a duration for the Thinking header:
// tenths of a second while short (matching the tool lines' precision),
// whole seconds once it crosses a minute.
func formatThinkingDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// tailLines returns the last max lines of value.
func tailLines(value string, max int) []string {
	lines := strings.Split(value, "\n")
	if len(lines) <= max {
		return lines
	}
	return lines[len(lines)-max:]
}
