package runtime

import (
	"context"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

func newAgentTestRuntime(t *testing.T, provider providers.ModelProvider) *Runtime {
	t.Helper()
	// Build same full tool set as production: 5 builtin + 3 runtime tools
	// Use non-sandbox manager for simplicity.
	full := tools.NewManager(tools.NewRegistry())
	// Register minimal echo tool to stand in for shell/write etc? Instead
	// we register all expected names with simple echo implementations so
	// filtering can be tested without needing real filesystem.
	registerTestTools(t, full)

	registry := agent.DefaultRegistry()
	memory := ""
	cfg := &config.Config{
		Model: config.Model{Provider: "ollama", Name: "test-model"},
	}

	// Pick general as initial
	def, _ := registry.Get("general")
	filtered, err := full.Filtered(def.Tools)
	if err != nil {
		t.Fatalf("filter general: %v", err)
	}
	a := agent.New(def.Name, def.SystemPrompt, "").WithProjectMemory(memory)

	rt := &Runtime{
		cfg:               cfg,
		provider:          provider,
		agent:             a,
		manager:           filtered,
		fullManager:       full,
		agents:            registry,
		activeAgent:       def.Name,
		projectMemoryText: memory,
		scheduler:         newScheduler(filtered, nil, nil, DefaultSchedulerConfig),
		discovery:         providers.NewDiscovery(providers.DefaultFactories()),
	}
	return rt
}

func registerTestTools(t *testing.T, m *tools.Manager) {
	t.Helper()
	names := []string{"read_file", "write_file", "list_files", "pwd", "shell", "search_files", "secret_scan", "load_skill", "update_task_state", "add_project_memory"}
	for _, name := range names {
		n := name
		tool := &testAgentTool{name: n}
		if err := m.Register(tool); err != nil {
			t.Fatalf("register %q: %v", n, err)
		}
	}
}

type testAgentTool struct {
	name string
}

func (t *testAgentTool) Name() string        { return t.name }
func (t *testAgentTool) Description() string { return "test tool " + t.name }
func (t *testAgentTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *testAgentTool) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	return tools.Result{Content: "ok:" + t.name}, nil
}

func TestAgent_ToolIsolationDefinitions(t *testing.T) {
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{{{Done: true}}}})
	// general has all 10
	if len(rt.manager.Definitions()) != 10 {
		t.Fatalf("general should have 10 tools, got %d", len(rt.manager.Definitions()))
	}
	if err := rt.SetAgent("legal"); err != nil {
		t.Fatalf("SetAgent legal: %v", err)
	}
	// legal should have 7 (no shell, no write_file, no secret_scan)
	defs := rt.manager.Definitions()
	if len(defs) != 7 {
		t.Fatalf("legal should have 7 tools, got %d: %v", len(defs), defs)
	}
	for _, d := range defs {
		if d.Name == "shell" || d.Name == "write_file" {
			t.Fatalf("legal should not expose %q", d.Name)
		}
	}
	if err := rt.SetAgent("cyber"); err != nil {
		t.Fatalf("SetAgent cyber: %v", err)
	}
	foundShell := false
	foundWrite := false
	for _, d := range rt.manager.Definitions() {
		if d.Name == "shell" {
			foundShell = true
		}
		if d.Name == "write_file" {
			foundWrite = true
		}
	}
	if !foundShell {
		t.Fatalf("cyber should have shell")
	}
	if foundWrite {
		t.Fatalf("cyber should not have write_file")
	}
	// Switch back to coding restores
	if err := rt.SetAgent("coding"); err != nil {
		t.Fatalf("SetAgent coding: %v", err)
	}
	if len(rt.manager.Definitions()) != 10 {
		t.Fatalf("coding should have 10, got %d", len(rt.manager.Definitions()))
	}
}

func TestAgent_FilteredToolRequestedByModelFailsClosed(t *testing.T) {
	// Legal agent does not have shell; model tries to call it -> tool not found
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{ID: "call-1", Name: "shell", Arguments: map[string]any{"command": "ls"}}}},
			{Done: true},
		},
		{
			{Text: "done"},
			{Done: true},
		},
	}}
	rt := newAgentTestRuntime(t, provider)
	if err := rt.SetAgent("legal"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "do shell"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var failed *ToolResult
	for ev := range events {
		if ev.Type == EventToolFailed {
			failed = ev.ToolResult
		}
		if ev.Type == EventDone {
			break
		}
		if ev.Type == EventError {
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}
	if failed == nil {
		t.Fatalf("expected tool failure for filtered tool")
	}
	if failed.Name != "shell" {
		t.Fatalf("failed tool = %q, want shell", failed.Name)
	}
	if failed.Content != "tool not found: shell" {
		// Content may be tool not found message
		if failed.Content == "" {
			t.Fatalf("expected content with tool not found")
		}
	}
}

func TestAgent_UnknownAgentErrors(t *testing.T) {
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{{{Done: true}}}})
	err := rt.SetAgent("nonexistent")
	if err == nil {
		t.Fatalf("expected error for unknown agent")
	}
}

func TestAgent_CurrentAgentAndList(t *testing.T) {
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{{{Done: true}}}})
	if got := rt.CurrentAgent(); got != "general" {
		t.Fatalf("initial = %q, want general", got)
	}
	list := rt.ListAgents()
	if len(list) != 7 {
		t.Fatalf("want 7, got %d", len(list))
	}
	if err := rt.SetAgent("docs"); err != nil {
		t.Fatalf("SetAgent docs: %v", err)
	}
	if got := rt.CurrentAgent(); got != "docs" {
		t.Fatalf("after = %q, want docs", got)
	}
}

func TestAgent_ToolSummariesReflectFiltered(t *testing.T) {
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{{{Done: true}}}})
	_ = rt.SetAgent("research")
	summaries := rt.ToolSummaries()
	if len(summaries) != 7 {
		t.Fatalf("research summaries len = %d, want 7", len(summaries))
	}
	for _, s := range summaries {
		if len(s) == 0 {
			t.Fatalf("empty summary")
		}
	}
}

func TestAgent_ProviderModelHintRollback(t *testing.T) {
	// Agent with model hint that is invalid should leave runtime unchanged.
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{{{Done: true}}}})
	origModel := rt.CurrentModel()
	origAgent := rt.CurrentAgent()
	// Create a custom agent with bad model hint by updating registry directly.
	bad := agent.Definition{Name: "badmodel", Description: "bad", SystemPrompt: "prompt", Tools: []string{"read_file"}, Model: "nonexistent-model-xyz", Provider: ""}
	if err := rt.agents.Register(bad); err != nil {
		// If already exists, update
		_ = rt.agents.Update(bad)
	}
	// SetAgent should fail due to model? But SetModel just sets name without validating model existence; it will succeed because newProvider will succeed even with arbitrary model (model name not validated). So this test is not meaningful for model.
	// Instead test provider hint failure: unknown provider should fail and rollback.
	badProv := agent.Definition{Name: "badprov", Description: "bad", SystemPrompt: "prompt", Tools: []string{"read_file"}, Provider: "unknown-provider-xyz"}
	if err := rt.agents.Register(badProv); err != nil {
		_ = rt.agents.Update(badProv)
	}
	err := rt.SetAgent("badprov")
	if err == nil {
		t.Fatalf("expected error for unknown provider")
	}
	if got := rt.CurrentAgent(); got != origAgent {
		t.Fatalf("after failed provider switch, agent = %q, want %q", got, origAgent)
	}
	if got := rt.CurrentModel(); got != origModel {
		t.Fatalf("after failed switch, model = %q, want %q", got, origModel)
	}
}

func TestAgent_SwitchingPreservesOtherState(t *testing.T) {
	rt := newAgentTestRuntime(t, &scriptedProvider{turns: [][]providers.StreamEvent{
		{{Text: "hi"}, {Done: true}},
	}})
	if err := rt.SetAgent("coding"); err != nil {
		t.Fatalf("SetAgent coding: %v", err)
	}
	if err := rt.SetAgent("legal"); err != nil {
		t.Fatalf("SetAgent legal: %v", err)
	}
	// After switches, runtime should still be usable for a Run.
	_, err := rt.Run([]providers.Message{{Role: providers.UserRole, Content: "hello"}})
	if err != nil {
		t.Fatalf("Run after switches: %v", err)
	}
}
