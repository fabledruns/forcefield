package tui

// Parent-side port of the wave3k-p2 child deliverable (kept its six test
// names and intent). The delivered draft was black-box style: it imported
// forcefield/internal/tui from inside package tui, invented exported
// types (AgentsState, GetRow) that do not exist, and applied terminal
// events without the delegate start that creates a row. Rewritten here
// against the real unexported API in agentsstate.go.

import (
	"errors"
	"strings"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// startCall returns a delegate start event for callID.
func startCall(callID string) runtime.Event {
	return runtime.Event{
		Type: runtime.EventToolStart,
		ToolCall: &providers.ToolCall{
			ID:        callID,
			Name:      "delegate",
			Arguments: map[string]any{"agent": "coding", "task": "task"},
		},
	}
}

const successContent = "[delegate] agent=\"coding\" turns=3 tool_calls=2 failed_tool_calls=0 denied=0\n" +
	"[delegate] child status=partial verification=verified phase=\"done\"\n\nbody text"

func TestAgentsTerminalFinishSuccessCompleted(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	event := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: true, Content: successContent,
	}}
	s.apply(event)
	row := s.byCall["call1"]
	if row.State != AgentCompleted {
		t.Errorf("Expected AgentCompleted, got %v", row.State)
	}
	if !strings.Contains(row.Status, "partial") {
		t.Errorf("Expected Status to contain 'partial', got %s", row.Status)
	}
}

func TestAgentsTerminalFinishErrorFailed(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	event := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: false, IsError: true,
	}}
	s.apply(event)
	row := s.byCall["call1"]
	if row.State != AgentFailed {
		t.Errorf("Expected AgentFailed, got %v", row.State)
	}
	if row.Detail == "" {
		t.Error("Expected Detail to be non-empty, got empty")
	}
}

func TestAgentsTerminalFailedErrDetail(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	err := errors.New("boom")
	event := runtime.Event{Type: runtime.EventToolFailed, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", IsError: true, Err: err,
	}}
	s.apply(event)
	row := s.byCall["call1"]
	if row.State != AgentFailed {
		t.Errorf("Expected AgentFailed, got %v", row.State)
	}
	if row.Detail != "boom" {
		t.Errorf("Expected Detail 'boom', got '%s'", row.Detail)
	}
}

func TestAgentsTerminalRejectionClassification(t *testing.T) {
	tests := map[string]AgentRowState{
		"unknown agent \"x\" (registry miss)":           AgentRejected,
		"task is 30000 characters, exceeding the limit": AgentRejected,
		"delegation cancelled before it started":        AgentCancelled,
		"child run blocked: stop boundary":              AgentFailed,
	}
	for content, expectedState := range tests {
		t.Run(content, func(t *testing.T) {
			s := &agentsState{}
			s.apply(startCall("call1"))
			// Production rejections arrive as soft failures, exactly as
			// softDelegateResult produces them; a SUCCESSFUL result whose
			// text merely mentions these words stays completed.
			event := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
				ToolCallID: "call1", Name: "delegate", Success: false, IsError: true, Content: content,
			}}
			s.apply(event)
			row := s.byCall["call1"]
			if row.State != expectedState {
				t.Errorf("Expected %v, got %v", expectedState, row.State)
			}
		})
	}
}

func TestAgentsTerminalTurnsExactWhenHeaderExceedsObserved(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	// The per-child progress budget let the panel observe turn 3 only.
	s.apply(runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{
		ToolCallID: "call1", Stream: "progress", Data: "[delegate: agent \"coding\" turn 3]",
	}})
	// The child's final header reports a higher total.
	s.apply(runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: true,
		Content: "[delegate] agent=\"coding\" turns=9 tool_calls=2 failed_tool_calls=0 denied=0\n" +
			"[delegate] child status=partial verification=verified phase=\"done\"\n\nbody text",
	}})

	row := s.byCall["call1"]
	if row.Turns != 3 {
		t.Errorf("Expected Turns 3 (the observed count), got %d", row.Turns)
	}
	if row.TurnsExact {
		t.Error("Expected TurnsExact false when the header exceeds what was observed")
	}
}

func TestAgentsTerminalFirstTerminalWins(t *testing.T) {
	s := &agentsState{}
	s.apply(startCall("call1"))
	failedEvent := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: false, IsError: true, Content: successContent,
	}}
	s.apply(failedEvent)
	successEvent := runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{
		ToolCallID: "call1", Name: "delegate", Success: true, Content: successContent,
	}}
	s.apply(successEvent)

	row := s.byCall["call1"]
	if row.State != AgentFailed {
		t.Errorf("Expected AgentFailed (first terminal wins), got %v", row.State)
	}
}
