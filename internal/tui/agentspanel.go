package tui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// agentsPanelMaxRows bounds how many child rows the panel shows at once.
// A delegation-heavy run cannot then crowd out the transcript: the newest
// rows win and the rest collapse into one "+N more" line.
const agentsPanelMaxRows = 8

// agentsPanelNarrowWidth is the width below which the panel drops the
// task column and switches to ASCII-only glyphs.
const agentsPanelNarrowWidth = 48

// agentsPanelMaxRuleWidth caps the accent rules so the header does not
// stretch absurdly on ultra-wide terminals.
const agentsPanelMaxRuleWidth = 72

// agentsPanelRows returns the rows the panel renders: all rows when they
// fit, otherwise the newest agentsPanelMaxRows (oldest first).
func (s *agentsState) agentsPanelRows() []*childRow {
	if len(s.rows) <= agentsPanelMaxRows {
		return s.rows
	}
	return s.rows[len(s.rows)-agentsPanelMaxRows:]
}

// agentsPanelHeight is the panel's terminal row count, so layout()
// shrinks the transcript by exactly what View renders: header rule, one
// row per visible child, an optional "+N more" line, closing rule. It
// must stay in sync with renderAgentsPanel.
func (m *model) agentsPanelHeight() int {
	if !m.agents.active() {
		return 0
	}
	hidden := len(m.agents.rows) - len(m.agents.agentsPanelRows())
	height := len(m.agents.agentsPanelRows()) + 2
	if hidden > 0 {
		height++
	}
	return height
}

// renderAgentsPanel renders the live delegate-children block. It is a
// pure function of state: no event, cache or model mutation happens
// here, so resize and streaming updates stay consistent. It returns ""
// whenever no child is queued or running, and the layout treats that as
// zero rows.
func (m *model) renderAgentsPanel(width int) string {
	if width <= 0 || !m.agents.active() {
		return ""
	}

	rows := m.agents.agentsPanelRows()
	hidden := len(m.agents.rows) - len(rows)
	narrow := width < agentsPanelNarrowWidth

	accent := lipgloss.NewStyle().Foreground(colorAccent)
	ruleWidth := width
	if ruleWidth > agentsPanelMaxRuleWidth {
		ruleWidth = agentsPanelMaxRuleWidth
	}

	var b strings.Builder
	if ruleWidth > len("agents")+1 {
		label := accent.Render("agents")
		b.WriteString(label + " " + accent.Render(strings.Repeat("─", ruleWidth-len("agents")-1)))
	} else {
		b.WriteString(accent.Render("agents"))
	}
	b.WriteString("\n")

	for _, row := range rows {
		b.WriteString(clipToWidth(renderAgentRow(row, width, narrow), width))
		b.WriteString("\n")
	}
	if hidden > 0 {
		b.WriteString("+" + strconv.Itoa(hidden) + " more")
		b.WriteString("\n")
	}
	b.WriteString(accent.Render(strings.Repeat("─", ruleWidth)))
	return b.String()
}

// renderAgentRow renders one child row:
// "<glyph> subagent-N <agent> <state> <k> turns <task>". An inexact turn
// count (the child's progress budget hid turns) renders as "<k>+
// turns". Narrow layouts drop the task column entirely.
func renderAgentRow(row *childRow, width int, narrow bool) string {
	glyph := agentGlyph(row.State, narrow)
	turns := strconv.Itoa(row.Turns)
	if !row.TurnsExact {
		turns += "+"
	}

	var b strings.Builder
	b.WriteString(glyph + " " + row.Label)
	if row.Agent != "" {
		b.WriteString(" " + row.Agent)
	}
	b.WriteString(" " + string(row.State) + " " + turns + " turns ")
	head := b.String()

	if narrow {
		return head
	}
	available := width - utf8.RuneCountInString(head)
	if available <= 0 {
		return head
	}
	task := row.Task
	if r := []rune(task); len(r) > available {
		if available <= 3 {
			task = ""
		} else {
			task = string(r[:available-3]) + "..."
		}
	}
	return head + task
}

// agentGlyph picks the state glyph. Wide layouts use the package's icon
// language (all text-presentation, never emoji); narrow ones fall back
// to ASCII so the panel stays readable on minimal terminals.
func agentGlyph(state AgentRowState, narrow bool) string {
	if narrow {
		switch state {
		case AgentRunning:
			return ">"
		case AgentQueued:
			return "-"
		case AgentCompleted:
			return "x"
		case AgentDenied, AgentRejected:
			return "!"
		default: // failed, cancelled
			return "x"
		}
	}
	switch state {
	case AgentRunning:
		return IconRunning.String()
	case AgentQueued:
		return IconIdle.String()
	case AgentCompleted:
		return IconSuccess.String()
	case AgentDenied, AgentRejected:
		return IconCancel.String()
	default: // failed, cancelled
		return IconFailure.String()
	}
}

// clipToWidth cuts s to at most width runes (display columns, not
// styled widths: rows are plain text except their glyph).
func clipToWidth(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	return string([]rune(s)[:width])
}
