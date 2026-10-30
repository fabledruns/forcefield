package runtime

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
)

func boolp(b bool) *bool { return &b }

func TestLimitsForAgent_Precedence(t *testing.T) {
	base := Limits{MaxIterations: 60, MaxToolCalls: 300, MaxConsecutiveFailures: 5}
	if got := limitsForAgent(nil, "coding", base); got != base {
		t.Errorf("nil cfg = %+v, want base", got)
	}
	cfg := &config.Config{}
	if got := limitsForAgent(cfg, "coding", base); got != base {
		t.Errorf("no profiles = %+v, want base", got)
	}
	if got := limitsForAgent(cfg, "nope", base); got != base {
		t.Errorf("unknown agent = %+v, want base", got)
	}
	cfg.Agents = map[string]config.AgentConfig{
		"coding": {MaxIterations: 7, MaxConsecutiveFailures: 2},
	}
	got := limitsForAgent(cfg, "coding", base)
	if got.MaxIterations != 7 || got.MaxConsecutiveFailures != 2 || got.MaxToolCalls != 300 {
		t.Errorf("profile overlay = %+v, want 7/300/2", got)
	}
	if got := limitsForAgent(cfg, "legal", base); got != base {
		t.Errorf("other agent = %+v, want base", got)
	}
}

func TestContextBudgetForAgent_Precedence(t *testing.T) {
	caps := providers.Capabilities{ContextWindow: 64000, MaxOutputTokens: 2048}
	cfg := &config.Config{}
	cfg.Agent.ContextWindow = 32000
	cfg.Agents = map[string]config.AgentConfig{
		"coding": {ContextWindow: 8000, ContextSummary: boolp(true)},
		"legal":  {ContextSummary: boolp(false)},
	}
	// Profile beats global beats caps.
	b := contextBudgetFromConfig(cfg, "gpt-4o-mini", "coding", caps)
	if b.Limit != 8000 || !b.Summarize {
		t.Errorf("coding = %+v, want limit 8000 + summary", b)
	}
	// No profile value: global wins over caps.
	b = contextBudgetFromConfig(cfg, "gpt-4o-mini", "legal", caps)
	if b.Limit != 32000 || b.Summarize {
		t.Errorf("legal = %+v, want global 32000 + explicit false", b)
	}
	// Neither: caps.
	empty := &config.Config{}
	b = contextBudgetFromConfig(empty, "gpt-4o-mini", "general", caps)
	if b.Limit != 64000 || b.Reserve != 2048 || b.Summarize {
		t.Errorf("fallback = %+v, want caps 64000/2048, no summary", b)
	}
}

// TestAgentProfileLimitsEnforced pins end-to-end profile scoping: the
// coding profile's tight iteration budget blocks its run while the same
// history under general proceeds.
func TestAgentProfileLimitsEnforced(t *testing.T) {
	newTurns := func() [][]providers.StreamEvent {
		echo := func(id string) []providers.StreamEvent {
			return []providers.StreamEvent{
				{ToolCalls: []providers.ToolCall{{ID: id, Name: "echo", Arguments: map[string]any{"value": "x"}}}},
				{Done: true},
			}
		}
		return [][]providers.StreamEvent{
			echo("c1"),
			echo("c2"),
			{{Text: "done"}, {Done: true}},
		}
	}
	build := func(agent string) (*Runtime, *scriptedProvider) {
		p := &scriptedProvider{turns: newTurns()}
		cfg := &config.Config{}
		cfg.Agents = map[string]config.AgentConfig{
			"coding": {MaxIterations: 1},
		}
		manager := newTestManager(t, echoTool{})
		return &Runtime{
			provider:    p,
			agent:       newTestRuntime(p).agent,
			manager:     manager,
			scheduler:   newScheduler(manager, nil, nil, DefaultSchedulerConfig),
			limits:      DefaultLimits,
			cfg:         cfg,
			activeAgent: agent,
		}, p
	}

	rt, p := build("coding")
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var blocked bool
	for ev := range events {
		if ev.Type == EventBlocked {
			blocked = true
		}
	}
	if !blocked {
		t.Error("coding run with max_iterations=1 did not block")
	}
	if p.calls != 1 {
		t.Errorf("coding provider calls = %d, want 1", p.calls)
	}

	rt, _ = build("general")
	events, err = rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var done bool
	for ev := range events {
		if ev.Type == EventBlocked {
			t.Fatalf("general run blocked under the coding profile budget: %v", ev.Err)
		}
		if ev.Type == EventDone {
			done = true
		}
	}
	if !done {
		t.Error("general run did not finish")
	}
}

// TestDelegateChildAgentProfileLimitsEnforced verifies that delegated
// child runs apply their own per-agent profile limits (e.g.,
// agents.<child>.max_iterations) rather than inheriting the parent
// snapshot's agent-specific limits.
func TestDelegateChildAgentProfileLimitsEnforced(t *testing.T) {
	t.Run("child profile max_iterations blocks child without blocking parent", func(t *testing.T) {
		turns := [][]providers.StreamEvent{
			// Parent turn 1: delegates to researcher
			{
				{ToolCalls: []providers.ToolCall{{
					ID:        "del-1",
					Name:      "delegate",
					Arguments: map[string]any{"agent": "researcher", "task": "investigate"},
				}}},
				{Done: true},
			},
			// Child turn 1: calls echo (allowed; child MaxIterations=1)
			{
				{ToolCalls: []providers.ToolCall{{
					ID:        "c-echo-1",
					Name:      "echo",
					Arguments: map[string]any{"value": "step-1"},
				}}},
				{Done: true},
			},
			// Parent turn 2: receives blocked child report and completes
			{
				{Text: "handled child block"},
				{Done: true},
			},
		}
		provider := &scriptedProvider{turns: turns}
		rt := newOrchestrationTestRuntime(t, provider)
		rt.limits = DefaultLimits
		rt.cfg = &config.Config{
			Agents: map[string]config.AgentConfig{
				"researcher": {MaxIterations: 1},
			},
		}

		events, err := rt.StreamChat(context.Background(), []providers.Message{
			{Role: providers.UserRole, Content: "run"},
		})
		if err != nil {
			t.Fatalf("StreamChat: %v", err)
		}
		finish, final := delegateRunSummary(events)
		if finish == nil {
			t.Fatal("missing delegate tool result")
		}
		if !finish.IsError || !strings.Contains(finish.Content, "child run blocked: stopped after 1 iterations") {
			t.Errorf("delegate result = %+v, want child blocked after 1 iteration", finish)
		}
		if final == nil || final.Content != "handled child block" {
			t.Errorf("parent final = %+v, want parent to complete normally", final)
		}
	})

	t.Run("parent profile max_iterations does not leak into child without override", func(t *testing.T) {
		// Parent ("root") has MaxIterations: 2, global base has MaxIterations: 10,
		// child ("researcher") has no override and takes 3 turns (2 tool turns + 1 final answer).
		turns := [][]providers.StreamEvent{
			// Parent turn 1: delegates to researcher
			{
				{ToolCalls: []providers.ToolCall{{
					ID:        "del-1",
					Name:      "delegate",
					Arguments: map[string]any{"agent": "researcher", "task": "three-turn task"},
				}}},
				{Done: true},
			},
			// Child turn 1
			{
				{ToolCalls: []providers.ToolCall{{
					ID:        "c-echo-1",
					Name:      "echo",
					Arguments: map[string]any{"value": "one"},
				}}},
				{Done: true},
			},
			// Child turn 2
			{
				{ToolCalls: []providers.ToolCall{{
					ID:        "c-echo-2",
					Name:      "echo",
					Arguments: map[string]any{"value": "two"},
				}}},
				{Done: true},
			},
			// Child turn 3 (exceeds parent's MaxIterations=2, within base MaxIterations=10)
			{
				{Text: "child finished on turn 3"},
				{Done: true},
			},
			// Parent turn 2: completes within parent's MaxIterations=2
			{
				{Text: "parent done"},
				{Done: true},
			},
		}
		provider := &scriptedProvider{turns: turns}
		rt := newOrchestrationTestRuntime(t, provider)
		rt.limits = Limits{MaxIterations: 10, MaxToolCalls: 100, MaxConsecutiveFailures: 5}
		rt.cfg = &config.Config{
			Agents: map[string]config.AgentConfig{
				"root": {MaxIterations: 2},
			},
		}

		events, err := rt.StreamChat(context.Background(), []providers.Message{
			{Role: providers.UserRole, Content: "run"},
		})
		if err != nil {
			t.Fatalf("StreamChat: %v", err)
		}
		finish, final := delegateRunSummary(events)
		if finish == nil || finish.IsError {
			t.Fatalf("delegate finish = %+v, want success on child turn 3", finish)
		}
		if !strings.Contains(finish.Content, "turns=3") || !strings.Contains(finish.Content, "child finished on turn 3") {
			t.Errorf("delegate content = %q, want turns=3 and child answer", finish.Content)
		}
		if final == nil || final.Content != "parent done" {
			t.Errorf("parent final = %+v, want parent done", final)
		}
	})

	t.Run("nested delegation resolves depth-1 and depth-2 profile limits independently", func(t *testing.T) {
		// Depth 0 ("root") has MaxIterations=2.
		// Depth 1 ("coding") has MaxIterations=2 and delegates to "researcher".
		// Depth 2 ("researcher") has no profile override and takes 3 turns (within base MaxIterations=10).
		turns := [][]providers.StreamEvent{
			// Root turn 1: delegates to coding
			{
				{ToolCalls: []providers.ToolCall{{
					ID:        "del-root-1",
					Name:      "delegate",
					Arguments: map[string]any{"agent": "coding", "task": "coordinate"},
				}}},
				{Done: true},
			},
			// Coding (depth 1) turn 1: delegates to researcher (depth 2)
			{
				{ToolCalls: []providers.ToolCall{{
					ID:        "del-coding-1",
					Name:      "delegate",
					Arguments: map[string]any{"agent": "researcher", "task": "deep dive"},
				}}},
				{Done: true},
			},
			// Researcher (depth 2) turn 1
			{
				{ToolCalls: []providers.ToolCall{{
					ID:        "r-echo-1",
					Name:      "echo",
					Arguments: map[string]any{"value": "one"},
				}}},
				{Done: true},
			},
			// Researcher (depth 2) turn 2
			{
				{ToolCalls: []providers.ToolCall{{
					ID:        "r-echo-2",
					Name:      "echo",
					Arguments: map[string]any{"value": "two"},
				}}},
				{Done: true},
			},
			// Researcher (depth 2) turn 3: finishes (exceeds coding's MaxIterations=2, allowed by base=10)
			{
				{Text: "researcher finished on turn 3"},
				{Done: true},
			},
			// Coding (depth 1) turn 2: finishes within its MaxIterations=2
			{
				{Text: "coding synthesized researcher report"},
				{Done: true},
			},
			// Root (depth 0) turn 2: finishes
			{
				{Text: "root complete"},
				{Done: true},
			},
		}
		provider := &scriptedProvider{turns: turns}
		rt := newOrchestrationTestRuntime(t, provider)
		if err := rt.orch.registry.Register(agent.Definition{
			Name:         "coding",
			Description:  "test coding agent",
			SystemPrompt: "coding-system",
			Tools:        []string{"echo"},
			Skills:       []string{},
		}); err != nil {
			t.Fatalf("register coding: %v", err)
		}
		rt.orch.resolved.maxDepth = 2
		rt.limits = Limits{MaxIterations: 10, MaxToolCalls: 100, MaxConsecutiveFailures: 5}
		rt.cfg = &config.Config{
			Agents: map[string]config.AgentConfig{
				"root":   {MaxIterations: 2},
				"coding": {MaxIterations: 2, ContextWindow: 16000},
			},
		}

		events, err := rt.StreamChat(context.Background(), []providers.Message{
			{Role: providers.UserRole, Content: "run nested"},
		})
		if err != nil {
			t.Fatalf("StreamChat: %v", err)
		}
		finish, final := delegateRunSummary(events)
		if finish == nil || finish.IsError {
			t.Fatalf("delegate finish = %+v, want success across nested chain", finish)
		}
		if !strings.Contains(finish.Content, "coding synthesized researcher report") {
			t.Errorf("delegate content = %q, want coding synthesis", finish.Content)
		}
		if final == nil || final.Content != "root complete" {
			t.Errorf("root final = %+v, want root complete", final)
		}
	})
}
