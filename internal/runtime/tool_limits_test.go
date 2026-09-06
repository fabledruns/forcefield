package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// cappedTool advertises an oversized timeout to prove the scheduler
// clamps it to the hard ceiling.
type ceilingTool struct{ meta tools.Metadata }

func (t *ceilingTool) Name() string        { return "ceiling" }
func (t *ceilingTool) Description() string { return "ceiling" }
func (t *ceilingTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *ceilingTool) Execute(context.Context, map[string]any) (tools.Result, error) {
	return tools.Result{Content: "ok"}, nil
}
func (t *ceilingTool) Metadata() tools.Metadata { return t.meta }

// overrideTool reports configured limits through LimitsProvider.
type overrideTool struct {
	limits tools.Limits
	ran    bool
}

func (t *overrideTool) Name() string        { return "capped" }
func (t *overrideTool) Description() string { return "capped" }
func (t *overrideTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *overrideTool) Execute(ctx context.Context, _ map[string]any) (tools.Result, error) {
	t.ran = true
	<-ctx.Done()
	return tools.Result{}, ctx.Err()
}
func (t *overrideTool) ToolLimits() tools.Limits { return t.limits }

// TestEffectiveTimeout_Ceiling pins the 300s hard ceiling: advertised,
// configured, or default timeouts above it clamp down.
func TestEffectiveTimeout_Ceiling(t *testing.T) {
	huge := &ceilingTool{meta: tools.Metadata{Timeout: time.Hour}}
	if got := effectiveTimeout(huge, huge.Metadata()); got != tools.MaxTimeout {
		t.Errorf("oversized = %v, want ceiling %v", got, tools.MaxTimeout)
	}
	def := &ceilingTool{meta: tools.Metadata{}}
	if got := effectiveTimeout(def, def.Metadata()); got != tools.DefaultToolTimeout {
		t.Errorf("unset = %v, want default %v", got, tools.DefaultToolTimeout)
	}
	normal := &ceilingTool{meta: tools.Metadata{Timeout: 5 * time.Second}}
	if got := effectiveTimeout(normal, normal.Metadata()); got != 5*time.Second {
		t.Errorf("normal = %v, want passthrough", got)
	}
}

// TestEffectiveTimeout_ConfiguredOverrideWins pins that a per-tool
// configured timeout flows through LimitsProvider into the execution
// bound (still clamped).
func TestEffectiveTimeout_ConfiguredOverrideWins(t *testing.T) {
	tool := &overrideTool{limits: tools.Limits{Timeout: 50 * time.Millisecond}}
	if got := effectiveTimeout(tool, tools.Metadata{Timeout: time.Hour}); got != 50*time.Millisecond {
		t.Errorf("override = %v, want 50ms (override beats metadata, under ceiling)", got)
	}
	tool.limits = tools.Limits{Timeout: time.Hour}
	if got := effectiveTimeout(tool, tools.Metadata{}); got != tools.MaxTimeout {
		t.Errorf("oversized override = %v, want ceiling", got)
	}
}

// TestScheduler_ConfiguredTimeoutTerminatesRun pins end-to-end timeout
// enforcement: a tool that never finishes is cut off by its configured
// bound and reported as a failure, not a hang.
func TestScheduler_ConfiguredTimeoutTerminatesRun(t *testing.T) {
	tool := &overrideTool{limits: tools.Limits{Timeout: 50 * time.Millisecond}}
	manager := newTestManager(t, tool)
	s := newScheduler(manager, nil, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	start := time.Now()
	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "capped"}},
		func(Event) bool { return true })
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("run took %v, want prompt timeout", elapsed)
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %+v, want one error result", results)
	}
	if !tool.ran {
		t.Error("tool never started")
	}
}

// TestToolLimitsFromConfig pins the config-to-limits mapping, including
// that an absent tools: block yields nil (defaults everywhere).
func TestToolLimitsFromConfig(t *testing.T) {
	if got := toolLimitsFromConfig(nil); got != nil {
		t.Errorf("nil config = %v, want nil", got)
	}
	cfg := &config.Config{}
	if got := toolLimitsFromConfig(cfg); got != nil {
		t.Errorf("empty tools = %v, want nil", got)
	}
	cfg.Tools = map[string]config.ToolLimits{
		"shell": {MaxBytes: 1024, TimeoutSeconds: 45},
	}
	got := toolLimitsFromConfig(cfg)
	if got["shell"].MaxBytes != 1024 || got["shell"].Timeout != 45*time.Second {
		t.Errorf("shell = %+v", got["shell"])
	}
}

// bigOutputTool returns far more than the runtime's 6000-character
// context guard to prove tool-level output is bounded before it, and the
// guard still applies after it.
type bigOutputTool struct{ body string }

func (t *bigOutputTool) Name() string        { return "big" }
func (t *bigOutputTool) Description() string { return "big" }
func (t *bigOutputTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *bigOutputTool) Execute(context.Context, map[string]any) (tools.Result, error) {
	return tools.Result{Content: t.body}, nil
}

// TestRun_ToolOutputBoundedBeforeContext pins the two-stage pipeline: a
// 20000-character tool result reaches the next provider turn truncated
// by the runtime guard with its marker, never whole.
func TestRun_ToolOutputBoundedBeforeContext(t *testing.T) {
	body := strings.Repeat("x", 20000)
	tool := &bigOutputTool{body: body}
	manager := newTestManager(t, tool)
	p := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "big", Arguments: map[string]any{}}}}, {Done: true}},
		{{Text: "done", Done: true}},
	}}
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
	}

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	for range events {
	}
	if len(p.messages) != 2 {
		t.Fatalf("provider turns = %d, want 2", len(p.messages))
	}
	var toolMsg *providers.Message
	for i := range p.messages[1] {
		if p.messages[1][i].Role == providers.ToolRole {
			toolMsg = &p.messages[1][i]
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool message in second turn")
	}
	if len(toolMsg.Content) >= len(body) {
		t.Errorf("tool content %d chars not bounded below %d", len(toolMsg.Content), len(body))
	}
	if !strings.Contains(toolMsg.Content, "truncated") {
		t.Error("bounded tool content lacks a truncation marker for the model")
	}
}
