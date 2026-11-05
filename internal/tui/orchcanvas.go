package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// This file connects the Phase 2 compositor (quadrant_view.go) to the
// live model state (PLAN §7, §8). It builds quadrant payloads only:
// every line describes something the runtime actually observed, as
// recorded by the event-folding in agentsstate.go. Nothing here
// invents agent activity — in particular there is no token-level
// subagent feed to show, so child quadrants render observed state,
// counters, and terminal summaries, exactly as the structured events
// carried them. Geometry, clipping, and the opening wordmark stay in
// the pure compositor.
//
// Every function here is read-only over the model: View has a value
// receiver and child rows are shared pointers across model copies, so
// slot assignment (assignQuadrantSlots) happens only in Update; this
// file reads the settled state.

// orchDimStyle renders standby and secondary lines in the plan's dim
// tone (PLAN §3.3 colorDim).
var orchDimStyle = lipgloss.NewStyle().Foreground(colorDim)

// mainAgentTailLines caps how many transcript lines Q0 offers the
// compositor. It is a content bound (the compositor remains the sole
// authority on geometry): a bounded tail keeps per-frame building cheap
// on tall terminals while still filling a full-height quadrant.
const mainAgentTailLines = 8

// orchFooterHeight is the footer height in orchestration mode. It is
// the classic footerHeight unchanged: that value already reserves the
// agents panel's rows, and in orchestration mode the canvas replaces
// both the transcript viewport and the panel, so reserving is exactly
// right — header + canvas(cH) + footer then total exactly m.height
// because the compositor returns exactly cH lines.
func (m model) orchFooterHeight() int {
	return m.footerHeight()
}

// renderOrchestrationScreen is the orchestration mode's whole body: the
// four-quadrant canvas between the unchanged header and footer. The
// footer (including the permission-modal prompt and notice strips) is
// the classic renderer, so overlays keep their existing behavior and
// priority. Canvas sizes clamp at zero; the compositor guarantees a
// panic-free exact-size output for any non-negative geometry.
func (m model) renderOrchestrationScreen() string {
	cW, cH := m.width, m.height-m.headerRows()-m.orchFooterHeight()
	if cW < 0 {
		cW = 0
	}
	if cH < 0 {
		cH = 0
	}
	var anim *openingAnimState
	if m.anim.Started() {
		anim = &m.anim
	}
	canvas := renderOrchestrationCanvas(cW, cH, anim, m.quadrantContents())
	return lipgloss.JoinVertical(lipgloss.Left, m.renderHeader(), canvas, m.renderFooter())
}

// quadrantContents builds the four quadrant payloads from live state.
// Q0 is the main agent; Q1..Q3 mirror the slot owners settled by
// assignQuadrantSlots in Update (nil = dim idle standby).
func (m model) quadrantContents() [4]quadrantContent {
	quads := [4]quadrantContent{
		m.mainQuadrant(),
	}
	for i, owner := range m.agents.slots {
		quads[i+1] = m.childQuadrant(i+1, owner)
	}
	return quads
}

// mainQuadrant builds Q0: the main agent's live state, provider/model
// badge, and the transcript's tail. The state label is the same
// three-state collapse the header uses (idle/thinking/running, plus
// waiting on a permission prompt); when tools are in flight, their
// names are shown — real observed activity, never a fabricated stream.
func (m model) mainQuadrant() quadrantContent {
	q := quadrantContent{Label: mainAgentLabel}
	q.Lines = append(q.Lines, orchDimStyle.Render(m.mainStateLine()))
	if badge := m.mainBadgeLine(); badge != "" {
		q.Lines = append(q.Lines, orchDimStyle.Render(badge))
	}
	q.Lines = append(q.Lines, m.mainTailLines()...)
	return q
}

// mainStateLine renders the main agent's observed state: the header's
// state label plus the names of tools currently in flight.
func (m model) mainStateLine() string {
	_, _, label := m.headerState()
	if names := m.mainActiveToolNames(); names != "" {
		return label + " · " + names
	}
	return label
}

// mainActiveToolNames names the tools with live status lines, sorted
// for determinism (the scheduler may run several concurrently). Only
// tool names are shown — never arguments or output.
func (m model) mainActiveToolNames() string {
	if len(m.activeTools) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(m.activeTools))
	var names []string
	for _, idx := range m.activeTools {
		if idx < 0 || idx >= len(m.entries) {
			continue
		}
		rec := m.entries[idx].Tool
		if rec == nil || rec.name == "" || seen[rec.name] {
			continue
		}
		seen[rec.name] = true
		names = append(names, rec.name)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	if len(names) > 2 {
		return strings.Join(names[:2], ", ") + " +" + strconv.Itoa(len(names)-2)
	}
	return strings.Join(names, ", ")
}

// mainBadgeLine renders the provider/model identity the header already
// shows, so the quadrant reads as the same agent.
func (m model) mainBadgeLine() string {
	if m.providerName == "" && m.modelName == "" {
		return ""
	}
	return m.providerName + "/" + m.modelName
}

// mainTailLines returns the transcript's last visible lines, trailing
// blanks trimmed, so Q0 shows what the main agent is actually saying.
// The tail mirrors real viewport content; nothing is invented. Empty
// while the transcript is empty (the splash shows instead).
func (m model) mainTailLines() []string {
	if len(m.entries) == 0 {
		return nil
	}
	lines := strings.Split(m.viewport.View(), "\n")
	for len(lines) > 0 && ansi.StringWidth(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > mainAgentTailLines {
		lines = lines[len(lines)-mainAgentTailLines:]
	}
	return lines
}

// childQuadrant builds one child slot's payload. Vacant slots render
// the stable subagent-N header with a dim standby line (PLAN §6.1);
// live slots render only what the events observed.
func (m model) childQuadrant(slot int, owner *childRow) quadrantContent {
	label := "subagent-" + strconv.Itoa(slot)
	if owner == nil {
		return quadrantContent{
			Label: label,
			Lines: []string{orchDimStyle.Render("idle · awaiting delegation")},
		}
	}
	// A reclaimed slot reports how many agents it already served
	// (PLAN §6.1 slot-reuse history).
	if n := m.agents.archivedCount(slot); n > 0 {
		label += fmt.Sprintf(" [%d archived]", n)
	}
	return quadrantContent{Label: label, Lines: childQuadrantLines(m.agents, owner)}
}

// childQuadrantLines renders one child's observed state. Every line is
// conditional: a queued row shows only its state word, a running row
// its live counters, a terminal row its summary — nothing is padded
// with assumptions. The overflow summary rides in Q3's body, which is
// where slots overflow first.
func childQuadrantLines(s agentsState, row *childRow) []string {
	var lines []string
	head := string(row.State)
	if row.Agent != "" {
		head += " · " + row.Agent
	}
	lines = append(lines, orchDimStyle.Render(head))

	if row.Turns > 0 {
		turns := strconv.Itoa(row.Turns)
		if !row.TurnsExact {
			turns += "+"
		}
		counters := turns + " turns"
		if row.ToolCalls > 0 {
			counters += fmt.Sprintf(" · %d tool call(s)", row.ToolCalls)
		}
		lines = append(lines, orchDimStyle.Render(counters))
	}
	if row.ActiveTool != "" {
		lines = append(lines, orchDimStyle.Render("tool: "+row.ActiveTool))
	}
	if row.DeniedCount > 0 || row.FailedCount > 0 {
		lines = append(lines, orchDimStyle.Render(fmt.Sprintf("denied %d · failed %d", row.DeniedCount, row.FailedCount)))
	}
	if lineage := row.lineageLine(); lineage != "" {
		lines = append(lines, orchDimStyle.Render(lineage))
	}
	// Task identity: the text when the stream feed carried it, else
	// only the length the structured event reported — task content is
	// never fabricated from a count (agentsstate.go TaskChars doc).
	switch {
	case row.Task != "":
		lines = append(lines, orchDimStyle.Render(row.Task))
	case row.TaskChars > 0:
		lines = append(lines, orchDimStyle.Render(fmt.Sprintf("task: %d chars requested", row.TaskChars)))
	}
	// Live child output: the actual streamed text the child produced.
	// Rendered as normal text (not dim) so it reads as the child's
	// work. Bounded by the row's own scrollback cap; completed rows
	// keep their final text.
	if out := row.outputLines(maxChildOutputLines); len(out) > 0 {
		lines = append(lines, out...)
	}
	// Terminal summary: the child's own status line when it completed,
	// the failure detail otherwise. First-terminal-wins already ran in
	// the state machine, so this is the settled outcome.
	if row.State.terminal() {
		switch row.State {
		case AgentCompleted:
			if row.Status != "" {
				lines = append(lines, orchDimStyle.Render(row.Status))
			}
		default:
			if row.Detail != "" {
				lines = append(lines, orchDimStyle.Render(row.Detail))
			}
		}
	} else if wait := s.nestedActiveChild(row); wait != nil {
		lines = append(lines, orchDimStyle.Render("waiting on "+wait.Label))
	}

	// The overflow summary lives at the bottom of Q3: agents beyond the
	// three slots are named there with their live turn counters.
	if row == s.slots[2] {
		if overflow := s.overflowLine(); overflow != "" {
			lines = append(lines, orchDimStyle.Render(overflow))
		}
	}
	return lines
}
