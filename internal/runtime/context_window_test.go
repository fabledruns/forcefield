package runtime

import (
	"strings"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

func TestBuildMessages_SlidingWindow(t *testing.T) {
	ag := agent.New("test", "system prompt", "")
	rt := &Runtime{agent: ag, manager: tools.NewManager(tools.NewRegistry())}

	// Build history with a goal + many turns
	var history []providers.Message
	history = append(history, providers.Message{Role: providers.UserRole, Content: "goal: do the thing"})
	// Add 500 more user/assistant pairs (1000 messages)
	for i := 0; i < 500; i++ {
		history = append(history, providers.Message{Role: providers.UserRole, Content: strings.Repeat("x", 10)})
		history = append(history, providers.Message{Role: providers.AssistantRole, Content: strings.Repeat("y", 10)})
	}
	if len(history) != 1001 {
		t.Fatalf("history len %d", len(history))
	}
	msgs := rt.buildMessages(history)
	// Should be bounded: system + maxContextMessages (goal preserved + tail)
	if len(msgs) > maxContextMessages+1 {
		t.Fatalf("buildMessages len %d exceeds bound %d", len(msgs), maxContextMessages+1)
	}
	if msgs[0].Role != providers.SystemRole {
		t.Fatalf("first msg role %v, want system", msgs[0].Role)
	}
	// Goal should be preserved as second message (after system)
	foundGoal := false
	for _, m := range msgs {
		if m.Content == "goal: do the thing" {
			foundGoal = true
			break
		}
	}
	if !foundGoal {
		t.Fatal("goal message not preserved in window")
	}
	// Recent history preserved: last message should be last of history
	lastHistory := history[len(history)-1]
	lastOut := msgs[len(msgs)-1]
	if lastOut.Content != lastHistory.Content {
		t.Fatalf("last message not preserved: got %q want %q", lastOut.Content, lastHistory.Content)
	}
}

func TestBuildMessages_Thousands(t *testing.T) {
	ag := agent.New("test", "sys", "")
	rt := &Runtime{agent: ag, manager: tools.NewManager(tools.NewRegistry())}
	var history []providers.Message
	history = append(history, providers.Message{Role: providers.UserRole, Content: "initial goal"})
	for i := 0; i < 3000; i++ {
		history = append(history, providers.Message{Role: providers.UserRole, Content: "u"})
		history = append(history, providers.Message{Role: providers.AssistantRole, Content: "a"})
	}
	msgs := rt.buildMessages(history)
	if len(msgs) > maxContextMessages+1 {
		t.Fatalf("thousands: len %d exceeds bound", len(msgs))
	}
	// Ensure system preserved
	if !strings.Contains(msgs[0].Content, "sys") {
		t.Fatalf("system prompt not preserved: %q", msgs[0].Content)
	}
}

func TestBuildMessages_SmallHistoryUnchanged(t *testing.T) {
	ag := agent.New("test", "sys", "")
	rt := &Runtime{agent: ag, manager: tools.NewManager(tools.NewRegistry())}
	history := []providers.Message{
		{Role: providers.UserRole, Content: "hi"},
		{Role: providers.AssistantRole, Content: "hello"},
	}
	msgs := rt.buildMessages(history)
	if len(msgs) != 3 { // system + 2
		t.Fatalf("small history len %d, want 3", len(msgs))
	}
	if msgs[1].Content != "hi" || msgs[2].Content != "hello" {
		t.Fatalf("small history content mismatch %#v", msgs)
	}
}

func TestBuildMessages_PreservesToolHistory(t *testing.T) {
	ag := agent.New("test", "sys", "")
	rt := &Runtime{agent: ag, manager: tools.NewManager(tools.NewRegistry())}
	var history []providers.Message
	history = append(history, providers.Message{Role: providers.UserRole, Content: "goal"})
	// Add 200 tool exchanges (assistant tool_calls + tool result)
	for i := 0; i < 200; i++ {
		history = append(history, providers.Message{Role: providers.AssistantRole, Content: "call", ToolCalls: []providers.ToolCall{{ID: "1", Name: "shell"}}})
		history = append(history, providers.Message{Role: providers.ToolRole, Content: "result", ToolCallID: "1", Name: "shell"})
	}
	msgs := rt.buildMessages(history)
	if len(msgs) > maxContextMessages+1 {
		t.Fatalf("tool history len %d exceeds bound", len(msgs))
	}
	// Check goal still present
	hasGoal := false
	for _, m := range msgs {
		if m.Content == "goal" {
			hasGoal = true
		}
	}
	if !hasGoal {
		t.Fatal("goal lost in tool-heavy window")
	}
}
