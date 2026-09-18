package runtime

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// TestPlanMode_WithholdsTools is the core plan-mode guarantee: even when
// the model requests a tool, a planning run fails the call closed and
// never executes it.
func TestPlanMode_WithholdsTools(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		toolCallTurn("call-1", "count"),
		textTurn("plan: do nothing"),
	}}
	tool := &countingTool{}
	rt := newTestRuntimeWithLimits(provider, DefaultLimits, tool)

	events, err := rt.StreamChatWithMode(
		context.Background(),
		[]providers.Message{{Role: providers.UserRole, Content: "plan this"}},
		ModePlan,
	)
	if err != nil {
		t.Fatalf("StreamChatWithMode() error = %v", err)
	}
	all, _, _ := collect(events)

	if got := tool.calls.Load(); got != 0 {
		t.Fatalf("plan-mode run executed the tool %d times, want 0", got)
	}
	found := false
	for _, e := range all {
		if e.Type == EventToolFailed && e.ToolResult != nil &&
			strings.Contains(e.ToolResult.Content, "tool not found: count") {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan-mode run has no tool-not-found failure: %+v", all)
	}
}

// TestPlanMode_OverlaysSystemPrompt asserts the planning constraint
// reaches the model on the first turn.
func TestPlanMode_OverlaysSystemPrompt(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		textTurn("plan: nothing to do"),
	}}
	rt := newTestRuntimeWithLimits(provider, DefaultLimits, &countingTool{})

	events, err := rt.StreamChatWithMode(
		context.Background(),
		[]providers.Message{{Role: providers.UserRole, Content: "plan this"}},
		ModePlan,
	)
	if err != nil {
		t.Fatalf("StreamChatWithMode() error = %v", err)
	}
	collect(events)

	if len(provider.messages) == 0 || len(provider.messages[0]) == 0 {
		t.Fatal("provider saw no messages")
	}
	if got := provider.messages[0][0].Content; !strings.Contains(got, "## Plan Mode") {
		t.Fatalf("plan-mode system prompt missing overlay: %q", got)
	}
}

// TestPlanMode_ChatKeepsPromptAndTools guards the normal path: chat mode
// neither overlays the prompt nor withholds the tool.
func TestPlanMode_ChatKeepsPromptAndTools(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		toolCallTurn("call-1", "count"),
		textTurn("done"),
	}}
	tool := &countingTool{}
	rt := newTestRuntimeWithLimits(provider, DefaultLimits, tool)

	events, err := rt.StreamChatWithMode(
		context.Background(),
		[]providers.Message{{Role: providers.UserRole, Content: "do it"}},
		ModeChat,
	)
	if err != nil {
		t.Fatalf("StreamChatWithMode() error = %v", err)
	}
	collect(events)

	if got := tool.calls.Load(); got != 1 {
		t.Fatalf("chat-mode run executed the tool %d times, want 1", got)
	}
	if got := provider.messages[0][0].Content; strings.Contains(got, "## Plan Mode") {
		t.Fatalf("chat-mode system prompt carries the plan overlay: %q", got)
	}
}

type readStubTool struct{ name string }

func (s readStubTool) Name() string        { return s.name }
func (s readStubTool) Description() string { return "stub" }
func (s readStubTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (s readStubTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	return tools.Result{Content: "ok"}, nil
}

// TestPlanManager_KeepsReadOnlyTools asserts the planning subset exactly:
// read-only tools stay, writers/executors go, order is preserved.
func TestPlanManager_KeepsReadOnlyTools(t *testing.T) {
	manager := tools.NewManager(tools.NewRegistry())
	for _, name := range []string{"read_file", "write_file", "shell", "shell_job", "git", "load_skill", "update_task_state"} {
		if err := manager.Register(readStubTool{name: name}); err != nil {
			t.Fatal(err)
		}
	}
	filtered, err := planManager(manager)
	if err != nil {
		t.Fatalf("planManager() error = %v", err)
	}
	var got []string
	for _, d := range filtered.Definitions() {
		got = append(got, d.Name)
	}
	want := []string{"read_file", "git", "load_skill"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("plan tool set = %v, want %v", got, want)
	}
}

// TestPlanMode_OverlayPersistsAcrossTurns asserts the per-iteration prompt
// refresh (which rebuilds from the agent) keeps the planning constraint.
func TestPlanMode_OverlayPersistsAcrossTurns(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		toolCallTurn("call-1", "count"),
		textTurn("plan: done"),
	}}
	rt := newTestRuntimeWithLimits(provider, DefaultLimits, &countingTool{})

	events, err := rt.StreamChatWithMode(
		context.Background(),
		[]providers.Message{{Role: providers.UserRole, Content: "plan this"}},
		ModePlan,
	)
	if err != nil {
		t.Fatalf("StreamChatWithMode() error = %v", err)
	}
	collect(events)

	if len(provider.messages) != 2 {
		t.Fatalf("provider turns = %d, want 2", len(provider.messages))
	}
	for i, turn := range provider.messages {
		if len(turn) == 0 || turn[0].Role != providers.SystemRole {
			t.Fatalf("turn %d has no system message: %+v", i, turn)
		}
		if !strings.Contains(turn[0].Content, "## Plan Mode") {
			t.Fatalf("turn %d system prompt lost the overlay: %q", i, turn[0].Content)
		}
	}
}
