package tui

// Parent-side rewrite of the wave3k2-r1 child deliverable (kept its six
// test names and assertion intents). The delivered draft redeclared
// model, childRow, AgentRowState and the agent constants inside the
// test file, invented mockAgentsState/mockEvent types, assigned a
// pointer to the value field m.agents, and stub-duplicated every test
// name. Rewritten against the real API in agentsstate.go/agentspanel.go.

import (
	"strings"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// panelModel builds a model with the panel state built from real
// delegate events, so render tests exercise the actual apply path.
func panelModel(task string) *model {
	m := &model{}
	m.agents.apply(runtime.Event{
		Type: runtime.EventToolStart,
		ToolCall: &providers.ToolCall{
			ID:        "c1",
			Name:      "delegate",
			Arguments: map[string]any{"agent": "coding", "task": task},
		},
	})
	return m
}

func TestAgentsPanelRenderInactive(t *testing.T) {
	m := &model{} // no delegate events: the panel stays hidden
	if got := m.renderAgentsPanel(120); got != "" {
		t.Errorf("Expected empty string for inactive model, got: %s", got)
	}
}

func TestAgentsPanelRenderZeroWidth(t *testing.T) {
	m := panelModel("draft the CLI evidence section")
	if got := m.renderAgentsPanel(0); got != "" {
		t.Errorf("Expected empty string for width 0, got: %s", got)
	}
}

func TestAgentsPanelRenderNarrowAsciiNoTask(t *testing.T) {
	m := panelModel("draft the CLI evidence section")
	m.agents.apply(runtime.Event{
		Type: runtime.EventToolProgress,
		ToolProgress: &runtime.ToolProgress{
			ToolCallID: "c1", Stream: "progress", Data: "[delegate: agent \"coding\" turn 2]",
		},
	})

	output := m.renderAgentsPanel(44)

	if strings.Contains(output, "draft the CLI evidence section") {
		t.Errorf("Narrow layout must drop the task column, got: %s", output)
	}
	if strings.ContainsAny(output, "●○✓✕⊘") {
		t.Errorf("Narrow layout must stay ASCII, got: %s", output)
	}
	if !strings.Contains(output, ">") {
		t.Errorf("Must contain ASCII running glyph '>', got: %s", output)
	}
	if lines := strings.Split(output, "\n"); len(lines) > 4 {
		t.Errorf("Narrow layout bound exceeded: %d lines", len(lines))
	}
}

func TestAgentsPanelRenderWideShowsTask(t *testing.T) {
	m := panelModel("draft the CLI evidence section")
	output := m.renderAgentsPanel(120)

	if !strings.Contains(output, "subagent-1") {
		t.Errorf("Output must contain 'subagent-1', got: %s", output)
	}
	if !strings.Contains(output, "draft the CLI evidence section") {
		t.Errorf("Output must contain the task text, got: %s", output)
	}
}

func TestAgentsPanelRenderRowFormat(t *testing.T) {
	m := panelModel("draft the CLI evidence section")
	m.agents.apply(runtime.Event{
		Type: runtime.EventToolProgress,
		ToolProgress: &runtime.ToolProgress{
			ToolCallID: "c1", Stream: "progress", Data: "[delegate: agent \"coding\" turn 2]",
		},
	})

	output := m.renderAgentsPanel(120)

	for _, want := range []string{"subagent-1", "coding", "running", "2 turns"} {
		if !strings.Contains(output, want) {
			t.Errorf("Output must contain %q, got: %s", want, output)
		}
	}
}

func TestAgentsPanelRenderCollapsesOnTerminal(t *testing.T) {
	m := panelModel("draft the CLI evidence section")
	// While the child runs the panel renders it...
	if got := m.renderAgentsPanel(120); !strings.Contains(got, "subagent-1") {
		t.Fatalf("expected the running row to render, got: %s", got)
	}
	// ...and a terminal row leaves the panel with exactly one bounded
	// transcript summary to drain in its place.
	m.agents.apply(runtime.Event{
		Type: runtime.EventToolFinish,
		ToolResult: &runtime.ToolResult{
			ToolCallID: "c1", Name: "delegate", Success: true,
			Content: "[delegate] agent=\"coding\" turns=2 tool_calls=1 failed_tool_calls=0 denied=0\n" +
				"[delegate] child status=partial verification=verified phase=\"done\"\n\nbody",
		},
	})
	if got := m.renderAgentsPanel(120); got != "" {
		t.Errorf("expected the panel to hide after the only row went terminal, got: %s", got)
	}
	summaries := m.agents.drainCollapsed()
	if len(summaries) != 1 || !strings.Contains(summaries[0], "subagent-1 completed") ||
		!strings.Contains(summaries[0], "status=partial") || !strings.Contains(summaries[0], "turns=2") {
		t.Errorf("unexpected summaries: %v", summaries)
	}
	if again := m.agents.drainCollapsed(); len(again) != 0 {
		t.Errorf("summary drained twice: %v", again)
	}
}

func TestAgentsPanelRenderTurnsInexact(t *testing.T) {
	m := panelModel("draft the CLI evidence section")
	m.agents.apply(runtime.Event{
		Type: runtime.EventToolProgress,
		ToolProgress: &runtime.ToolProgress{
			ToolCallID: "c1", Stream: "progress", Data: "[delegate: agent \"coding\" turn 1]",
		},
	})
	row := m.agents.rows[0]
	row.TurnsExact = false

	output := m.renderAgentsPanel(120)

	if !strings.Contains(output, "1+ turns") {
		t.Errorf("Output must contain '1+ turns' for inexact turns, got: %s", output)
	}
}
