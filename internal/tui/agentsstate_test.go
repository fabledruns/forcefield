package tui

import (
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

func TestAgentsStateDuplicateStartOneRow(t *testing.T) {
	s := &agentsState{}
	e1 := runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "c1", Name: "delegate", Arguments: map[string]any{"agent": "coding", "task": "line one\\nline two"}}}
	e2 := runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "c1", Name: "delegate", Arguments: map[string]any{"agent": "coding", "task": "line one\\nline two"}}}
	s.apply(e1)
	s.apply(e2)
	if len(s.rows) != 1 || s.rows[0].Label != "subagent-1" || s.rows[0].State != AgentQueued || !s.rows[0].TurnsExact {
		t.Errorf("Expected one row, Label 'subagent-1', State AgentQueued, TurnsExact true. Got %v", s.rows)
	}
}

func TestAgentsStateProgressCumulative(t *testing.T) {
	s := &agentsState{}
	e1 := runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "c1", Name: "delegate", Arguments: map[string]any{"agent": "coding", "task": "task"}}}
	s.apply(e1)
	p1 := runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{ToolCallID: "c1", Stream: "progress", Data: "[delegate: agent \"coding\" turn 1]"}}
	s.apply(p1)
	p2 := runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{ToolCallID: "c1", Stream: "progress", Data: "[delegate: agent \"coding\" turn 2]"}}
	s.apply(p2)
	p3 := runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{ToolCallID: "c1", Stream: "progress", Data: "[delegate: agent \"coding\" turn 3]"}}
	s.apply(p3)
	if s.rows[0].Turns != 3 || s.rows[0].State != AgentRunning {
		t.Errorf("Expected Turns 3, State AgentRunning. Got Turns %d, State %s", s.rows[0].Turns, s.rows[0].State)
	}
}

func TestAgentsStateProgressAfterTerminalIgnored(t *testing.T) {
	s := &agentsState{}
	e1 := runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "c1", Name: "delegate", Arguments: map[string]any{"agent": "coding", "task": "task"}}}
	s.apply(e1)
	t1 := runtime.Event{Type: runtime.EventToolDenied, ToolResult: &runtime.ToolResult{ToolCallID: "c1", Name: "delegate"}}
	s.apply(t1)
	p9 := runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{ToolCallID: "c1", Stream: "progress", Data: "[delegate: agent \"coding\" turn 9]"}}
	s.apply(p9)
	if s.rows[0].State != AgentDenied || s.rows[0].Turns != 0 {
		t.Errorf("Expected State AgentDenied, Turns 0. Got State %s, Turns %d", s.rows[0].State, s.rows[0].Turns)
	}
}

func TestAgentsStateDeniedAndCancelled(t *testing.T) {
	s := &agentsState{}
	e1 := runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "c1", Name: "delegate", Arguments: map[string]any{"agent": "coding", "task": "task"}}}
	s.apply(e1)
	t1 := runtime.Event{Type: runtime.EventToolDenied, ToolResult: &runtime.ToolResult{ToolCallID: "c1", Name: "delegate"}}
	s.apply(t1)

	e2 := runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "c2", Name: "delegate", Arguments: map[string]any{"agent": "other", "task": "task2"}}}
	s.apply(e2)
	t2 := runtime.Event{Type: runtime.EventToolCancelled, ToolResult: &runtime.ToolResult{ToolCallID: "c2", Name: "delegate"}}
	s.apply(t2)

	if s.byCall["c1"].State != AgentDenied || s.byCall["c2"].State != AgentCancelled {
		t.Errorf("Expected c1 AgentDenied, c2 AgentCancelled. Got c1: %s, c2: %s", s.byCall["c1"].State, s.byCall["c2"].State)
	}
}

func TestAgentsStateActive(t *testing.T) {
	s := &agentsState{}
	e1 := runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "c1", Name: "delegate", Arguments: map[string]any{"agent": "coding", "task": "task"}}}
	s.apply(e1) // queued true
	if !s.active() {
		t.Errorf("Expected active=true when queued exists")
	}

	t1 := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{ToolCallID: "c1", Name: "delegate", Success: true, IsError: false, Content: "done"}}
	s.apply(t1) // terminal true
	if s.active() {
		t.Errorf("Expected active=false when all rows are terminal")
	}
}

func TestAgentsStateZeroValueNoPanic(t *testing.T) {
	var s agentsState
	e1 := runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "c1", Name: "delegate", Arguments: map[string]any{"agent": "coding", "task": "task"}}}
	s.apply(e1)
	// apply(e1) should initialize s.rows and s.byCall without panic
	if len(s.rows) == 0 {
		t.Errorf("Expected state to be initialized after apply")
	}
	if !s.active() {
		t.Errorf("Expected active=true after apply")
	}
}

func TestAgentsStateProgressLastTurnWins(t *testing.T) {
	s := &agentsState{}
	e1 := runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "c1", Name: "delegate", Arguments: map[string]any{"agent": "coding", "task": "task"}}}
	s.apply(e1)
	p1 := runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{ToolCallID: "c1", Stream: "progress", Data: "[delegate: agent \"turn 5\" turn 2]"}}
	s.apply(p1) // Should take 2, not 5
	if s.rows[0].Turns != 2 {
		t.Errorf("Expected Turns 2, got %d", s.rows[0].Turns)
	}
}
