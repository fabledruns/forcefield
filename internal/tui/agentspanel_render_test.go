package tui

// Parent-side rewrite of the wave3k2-r1 child deliverable (kept its six
// test names and assertion intents). The delivered draft redeclared
// model, childRow, AgentRowState and the agent constants inside the
// test file, invented mockAgentsState/mockEvent types, assigned a
// pointer to the value field m.agents, and stub-duplicated every test
// name. Rewritten against the real API in agentsstate.go/agentspanel.go.

import (
	"strconv"
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

func TestAgentsPanelRenderTerminalSiblingLeavesPanel(t *testing.T) {
	m := panelModel("first task")
	m.agents.apply(runtime.Event{
		Type: runtime.EventToolStart,
		ToolCall: &providers.ToolCall{
			ID:        "c2",
			Name:      "delegate",
			Arguments: map[string]any{"agent": "research", "task": "second task"},
		},
	})
	// Finish c1 while c2 remains active: c1 must leave the panel and
	// collapse into the transcript summary while c2 continues rendering.
	m.agents.apply(runtime.Event{
		Type: runtime.EventToolFinish,
		ToolResult: &runtime.ToolResult{
			ToolCallID: "c1", Name: "delegate", Success: true,
			Content: "[delegate] agent=\"coding\" turns=2 tool_calls=1 failed_tool_calls=0 denied=0\n" +
				"[delegate] child status=verified\n\ndone",
		},
	})

	output := m.renderAgentsPanel(120)
	if strings.Contains(output, "subagent-1") {
		t.Errorf("terminal row subagent-1 should leave the panel while sibling runs, got:\n%s", output)
	}
	if !strings.Contains(output, "subagent-2") {
		t.Errorf("active row subagent-2 should remain in the panel, got:\n%s", output)
	}
	if got := m.agentsPanelHeight(); got != 3 {
		t.Errorf("agentsPanelHeight() = %d, want 3 (header + 1 active row + footer rule)", got)
	}
}

func TestAgentsPanelRenderRowCapAndMore(t *testing.T) {
	m := &model{}
	for i := 1; i <= 10; i++ {
		id := "c" + strconv.Itoa(i)
		m.agents.apply(runtime.Event{
			Type: runtime.EventToolStart,
			ToolCall: &providers.ToolCall{
				ID:        id,
				Name:      "delegate",
				Arguments: map[string]any{"agent": "coding", "task": "task"},
			},
		})
	}
	output := m.renderAgentsPanel(120)
	if !strings.Contains(output, "+2 more") {
		t.Errorf("expected '+2 more' for 10 active rows (cap 8), got:\n%s", output)
	}
	if strings.Contains(output, "subagent-1 ") || strings.Contains(output, "subagent-2 ") {
		t.Errorf("oldest overflow rows should be hidden under +2 more, got:\n%s", output)
	}
	if !strings.Contains(output, "subagent-10") {
		t.Errorf("newest row subagent-10 must be visible, got:\n%s", output)
	}
	if got := m.agentsPanelHeight(); got != 11 {
		t.Errorf("agentsPanelHeight() = %d, want 11 (header + 8 rows + '+2 more' + rule)", got)
	}
}

func TestAgentsPanelRepeatedWavesDoNotLeakTerminalRows(t *testing.T) {
	m := &model{}
	// Wave 1: spawn 8 children and finish all 8.
	for i := 1; i <= 8; i++ {
		id := "w1-" + strconv.Itoa(i)
		m.agents.apply(runtime.Event{
			Type: runtime.EventToolStart,
			ToolCall: &providers.ToolCall{
				ID:        id,
				Name:      "delegate",
				Arguments: map[string]any{"agent": "coding", "task": "wave 1"},
			},
		})
		m.agents.apply(runtime.Event{
			Type: runtime.EventToolFinish,
			ToolResult: &runtime.ToolResult{
				ToolCallID: id, Name: "delegate", Success: true,
				Content: "[delegate] agent=\"coding\" turns=1 tool_calls=0 failed_tool_calls=0 denied=0\n[delegate] child status=verified\n\nok",
			},
		})
	}
	if m.agents.active() {
		t.Fatal("panel should be inactive between waves")
	}
	_ = m.agents.drainCollapsed()

	// Wave 2: spawn 1 new child; it must be labeled subagent-9 and render
	// alone without "+N more" from the 8 retired wave-1 rows.
	m.agents.apply(runtime.Event{
		Type: runtime.EventToolStart,
		ToolCall: &providers.ToolCall{
			ID:        "w2-1",
			Name:      "delegate",
			Arguments: map[string]any{"agent": "research", "task": "wave 2"},
		},
	})
	output := m.renderAgentsPanel(120)
	if !strings.Contains(output, "subagent-9") {
		t.Errorf("expected wave 2 child to be labeled subagent-9, got:\n%s", output)
	}
	if strings.Contains(output, "more") {
		t.Errorf("retired wave-1 rows must not trigger '+N more' in wave 2, got:\n%s", output)
	}
	if got := m.agentsPanelHeight(); got != 3 {
		t.Errorf("agentsPanelHeight() in wave 2 = %d, want 3", got)
	}
}

func TestAgentsPanelStopStreamRetiresOpenRows(t *testing.T) {
	m := newTestModel()
	m.agents.apply(runtime.Event{
		Type: runtime.EventToolStart,
		ToolCall: &providers.ToolCall{
			ID:        "c1",
			Name:      "delegate",
			Arguments: map[string]any{"agent": "coding", "task": "task"},
		},
	})
	if !m.agents.active() {
		t.Fatal("expected agents panel to be active before stopStream")
	}
	m.stopStream(false)
	if m.agents.active() {
		t.Error("expected stopStream to retire open delegate rows so the panel hides")
	}
	if got := m.renderAgentsPanel(120); got != "" {
		t.Errorf("renderAgentsPanel after stopStream = %q, want empty", got)
	}
}

func TestAgentsPanelLayoutReservesAndRestoresViewportHeight(t *testing.T) {
	m := newTestModel()
	m.width = 120
	m.height = 30
	m.layout()
	baseViewportHeight := m.viewport.Height

	// Start a delegate child via streamEventMsg: viewport height must shrink by 3 lines.
	updated, _ := m.Update(streamEventMsg{
		gen: m.streamGen,
		Event: runtime.Event{
			Type: runtime.EventToolStart,
			ToolCall: &providers.ToolCall{
				ID:        "c1",
				Name:      "delegate",
				Arguments: map[string]any{"agent": "coding", "task": "check layout"},
			},
		},
	})
	m1 := updated.(model)
	if got := m1.viewport.Height; got != baseViewportHeight-3 {
		t.Errorf("viewport.Height with 1 active child = %d, want %d", got, baseViewportHeight-3)
	}

	// Finish the delegate child: viewport height must return to baseViewportHeight.
	updated2, _ := m1.Update(streamEventMsg{
		gen: m1.streamGen,
		Event: runtime.Event{
			Type: runtime.EventToolFinish,
			ToolResult: &runtime.ToolResult{
				ToolCallID: "c1", Name: "delegate", Success: true,
				Content: "[delegate] agent=\"coding\" turns=1 tool_calls=0 failed_tool_calls=0 denied=0\n[delegate] child status=verified\n\ndone",
			},
		},
	})
	m2 := updated2.(model)
	if got := m2.viewport.Height; got != baseViewportHeight {
		t.Errorf("viewport.Height after child finishes = %d, want %d", got, baseViewportHeight)
	}
}
