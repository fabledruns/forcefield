package runtime

import (
	"context"
	"testing"

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
