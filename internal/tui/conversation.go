package tui

import (
	"fmt"
	"strconv"
	"strings"
)

// role identifies who authored a chatEntry.
type role int

const (
	roleUser role = iota
	roleAssistant
	roleActivity
	roleError
	roleSystem // output from a slash command, e.g. /help or /model
)

// chatEntry is one turn in the visible transcript: something the user
// typed, a reply from the model, or an error that happened while getting
// one. Errors are rendered inline rather than crashing the session.
type chatEntry struct {
	Role      role
	Content   string
	Streaming bool

	// Turn groups entries created during one user request's run (stamped
	// from the stream generation at creation). Consecutive entries with
	// the same non-zero turn render as one compact visual block with a
	// single assistant header. Zero means ungrouped: resumed history,
	// errors, and permission prompts, which render exactly as before.
	Turn uint64

	// Tool, when set on a roleActivity entry, holds the structured details
	// behind the one-line tool summary so it can be expanded (ctrl+e).
	Tool *toolRecord

	// Thinking, when set on a roleActivity entry, holds the reasoning a
	// reasoning-capable model streamed before its answer, so it can be
	// expanded (ctrl+r) instead of living in the assistant message.
	Thinking *thinkingRecord
}

// contentSpan records which transcript content rows belong to one
// interactive entry, so mouse clicks can be resolved to tool/thinking
// blocks without fragile coordinate math at interaction time. Spans are
// rebuilt whenever the transcript re-renders.
type contentSpan struct {
	id        string // stable region id, also used for hover ("tool:<n>")
	entry     int    // index into entries
	startLine int    // first content row covered by the entry
	lines     int    // how many rows it occupies
	action    mouseAction
}

// regionID names a transcript hit region.
func regionID(kind string, entry int) string {
	return kind + ":" + strconv.Itoa(entry)
}

// rowsBetweenEntries is how many rows separate the end of one entry's
// rendered block from the start of the next. strings.Join(rendered,
// "\n\n") emits two newline characters between blocks: the first
// terminates the previous block's last line, the second creates exactly
// ONE blank separator row, and the next block begins on the row after it.
// Getting this wrong shifts every hit region down by one row per
// preceding entry, so clicks land below the visible text.
const rowsBetweenEntries = 1

// groupStarts reports, per entry, whether it begins a new visual block:
// index 0, entries with turn 0, and entries whose turn differs from the
// previous entry's. Consecutive entries sharing a non-zero turn (one user
// request's run) form one compact block with a single assistant header.
// Group membership is append-stable — appending entries never reclassifies
// earlier boundaries — so incremental rendering and mouse spans stay
// correct while events stream in.
func groupStarts(entries []chatEntry) []bool {
	starts := make([]bool, len(entries))
	for i, e := range entries {
		if i == 0 || e.Turn == 0 || entries[i-1].Turn != e.Turn {
			starts[i] = true
		}
	}
	return starts
}

// render turns a chatEntry into its final styled, word-wrapped form.
// width is the available content width in the viewport; hovered marks the
// block under the pointer for subtle emphasis.
func (e chatEntry) render(width int, hovered bool) string {
	return e.renderGrouped(width, hovered, true)
}

// renderGrouped renders like render, except a non-first assistant entry
// of a turn group (showHeader false) emits only its body, so one model
// turn stays one coherent visual block instead of repeating the
// Forcefield header for every text fragment after a tool batch.
func (e chatEntry) renderGrouped(width int, hovered, showHeader bool) string {
	var label string
	switch e.Role {
	case roleUser:
		label = userLabelStyle.Render("You")
	case roleAssistant:
		body := renderMarkdown(e.Content, width, e.Streaming)
		if !showHeader {
			return body
		}
		label = assistantLabelStyle.Render("Forcefield")
		return fmt.Sprintf("%s\n%s", label, body)
	case roleActivity:
		if e.Thinking != nil {
			return renderThinking(e.Thinking, width, hovered)
		}
		if e.Tool != nil {
			return e.renderTool(width, hovered)
		}
		return activityStyle.Width(width).Render(e.Content)
	case roleError:
		label = errorLabelStyle.Render("Error")
	case roleSystem:
		label = systemLabelStyle.Render("System")
	}

	body := messageBodyStyle.Width(width).Render(e.Content)
	return fmt.Sprintf("%s\n%s", label, body)
}

// renderTool renders one tool-call block: a collapsible summary line that
// doubles as the click target, plus the detail view when expanded.
func (e chatEntry) renderTool(width int, hovered bool) string {
	style := toolStatusStyle(e.Tool)
	caret := IconCollapsed
	if e.Tool.expanded {
		caret = IconExpanded
	}
	line := fmt.Sprintf("%s %s", caret, e.Content)
	if hovered {
		line = hoverEmphasisStyle.Render(line)
	} else {
		line = style.Render(line)
	}
	if !e.Tool.expanded {
		return style.Width(width).Render(line)
	}
	return style.Width(width).Render(line + formatToolDetails(e.Tool, width))
}

// renderTranscript joins the full entry history into the text shown in the
// scrollable viewport, returning no layout information (for callers that
// only need the bytes).
func renderTranscript(entries []chatEntry, width int) string {
	s, _ := renderTranscriptWithLayout(entries, width, "")
	return s
}

// renderTranscriptWithLayout renders the transcript and reports the
// content-space spans of every interactive entry (tool and thinking
// blocks), in order. Before the first message it shows the FORCEFIELD
// splash instead of an empty pane and yields no spans.
func renderTranscriptWithLayout(entries []chatEntry, width int, hoverID string) (string, []contentSpan) {
	if len(entries) == 0 {
		return renderBanner(width), nil
	}

	rendered := make([]string, 0, len(entries))
	spans := make([]contentSpan, 0)
	starts := groupStarts(entries)
	line := 0
	for i, e := range entries {
		hovered := false
		var action mouseAction
		switch {
		case e.Tool != nil:
			hovered = hoverID == regionID("tool", i)
			action = actionToggleTool
		case e.Thinking != nil:
			hovered = hoverID == regionID("think", i)
			action = actionToggleThinking
		default:
			action = actionNone
		}

		block := e.renderGrouped(width, hovered, starts[i] || e.Role != roleAssistant)
		lines := strings.Count(block, "\n") + 1
		rendered = append(rendered, block)

		if action != actionNone {
			spans = append(spans, contentSpan{
				id:        regionID(spanKind(action), i),
				entry:     i,
				startLine: line,
				lines:     lines,
				action:    action,
			})
		}
		// Step to the next block: a blank separator row between visual
		// blocks, none between members of one turn group.
		step := rowsBetweenEntries
		if i+1 < len(entries) && !starts[i+1] {
			step = 0
		}
		line += lines + step
	}
	return joinGrouped(rendered, starts), spans
}

// joinGrouped joins rendered blocks with a blank row between visual
// blocks and a single newline between members of one turn group.
func joinGrouped(rendered []string, starts []bool) string {
	var b strings.Builder
	for i, block := range rendered {
		if i > 0 {
			if starts[i] {
				b.WriteString("\n\n")
			} else {
				b.WriteString("\n")
			}
		}
		b.WriteString(block)
	}
	return b.String()
}

// spanKind maps an interactive action to its region-id prefix.
func spanKind(a mouseAction) string {
	if a == actionToggleThinking {
		return "think"
	}
	return "tool"
}
