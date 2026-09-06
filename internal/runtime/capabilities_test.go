package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// capsScriptedProvider is a scripted provider that also reports
// capabilities, so tests can negotiate without network.
type capsScriptedProvider struct {
	*scriptedProvider
	caps providers.Capabilities
}

func (p *capsScriptedProvider) Capabilities() providers.Capabilities { return p.caps }

// defsCapturingProvider records the tool definitions offered each turn.
type defsCapturingProvider struct {
	*scriptedProvider
	mu   sync.Mutex
	defs [][]tools.Definition
}

func (p *defsCapturingProvider) StreamChat(ctx context.Context, messages []providers.Message, defs []tools.Definition) (<-chan providers.StreamEvent, error) {
	p.mu.Lock()
	p.defs = append(p.defs, defs)
	p.mu.Unlock()
	return p.scriptedProvider.StreamChat(ctx, messages, defs)
}

func TestNegotiated_NoToolSupportWithholdsDefs(t *testing.T) {
	inner := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{Text: "no tools here", Done: true}},
	}}
	p := &defsCapturingProvider{scriptedProvider: inner}
	// Swap in a reporting no-tools provider.
	np := &noToolsProvider{defsCapturingProvider: p}
	rt := newTestRuntime(np)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range events {
	}
	if len(p.defs) != 1 {
		t.Fatalf("turns = %d, want 1", len(p.defs))
	}
	if len(p.defs[0]) != 0 {
		t.Errorf("defs sent = %d, want 0 when tool calling is not reported", len(p.defs[0]))
	}
}

// noToolsProvider reports streaming without tool calling.
type noToolsProvider struct {
	*defsCapturingProvider
}

func (p *noToolsProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Streaming: true, ToolCalling: false}
}

// TestNegotiated_UnreportedProviderKeepsDefs pins the compatibility
// rule: providers too old to report keep receiving definitions.
func TestNegotiated_UnreportedProviderKeepsDefs(t *testing.T) {
	inner := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{
			ToolCalls: []providers.ToolCall{{ID: "c1", Name: "echo", Arguments: map[string]any{"value": "x"}}},
		}, {Done: true}},
		{{Text: "done", Done: true}},
	}}
	p := &defsCapturingProvider{scriptedProvider: inner}
	rt := newTestRuntime(p)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range events {
	}
	if len(p.defs) == 0 || len(p.defs[0]) == 0 {
		t.Fatalf("defs = %v, want definitions for unreported providers", p.defs)
	}
}

// TestNegotiated_NoParallelRunsSequentially pins that a provider
// without parallel tool support gets strictly ordered execution: the
// second call starts only after the first finishes.
func TestNegotiated_NoParallelRunsSequentially(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 2)
	finished := make(chan string, 2)
	manager := newTestManager(t,
		&orderTool{name: "a", release: release, started: started, finished: finished},
		&orderTool{name: "b", release: release, started: started, finished: finished},
	)
	inner := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{
			ToolCalls: []providers.ToolCall{
				{ID: "1", Name: "a"},
				{ID: "2", Name: "b"},
			},
		}, {Done: true}},
		{{Text: "done", Done: true}},
	}}
	p := &capsScriptedProvider{
		scriptedProvider: inner,
		caps:             providers.Capabilities{Streaming: true, ToolCalling: true, ParallelToolCalls: false},
	}
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, SchedulerConfig{MaxConcurrency: 4, MaxRetries: 0, BaseBackoff: time.Millisecond}),
		limits:    DefaultLimits,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
		if err != nil {
			return
		}
		for range events {
		}
	}()

	// First tool starts; the second must wait even though the scheduler
	// allows 4-wide: capabilities force sequential. Either tool may win
	// the single slot; what matters is no overlap.
	var first string
	select {
	case first = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first tool never started")
	}
	select {
	case name := <-started:
		t.Fatalf("second tool started (%q) while %q still held the single slot: parallel despite no parallel support", name, first)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish after release")
	}
}

// orderTool records start/finish ordering for the sequential test.
type orderTool struct {
	name     string
	release  <-chan struct{}
	started  chan<- string
	finished chan<- string
}

func (t *orderTool) Name() string        { return t.name }
func (t *orderTool) Description() string { return "order" }
func (t *orderTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *orderTool) Execute(ctx context.Context, _ map[string]any) (tools.Result, error) {
	t.started <- t.name
	select {
	case <-t.release:
	case <-ctx.Done():
		return tools.Result{}, ctx.Err()
	}
	t.finished <- t.name
	return tools.Result{Content: t.name + "-done"}, nil
}

// TestBudgetForCaps_Precedence pins override > provider > table.
func TestBudgetForCaps_Precedence(t *testing.T) {
	reported := providers.Capabilities{ContextWindow: 64000, MaxOutputTokens: 2048}
	b := BudgetForCaps("gpt-4o-mini", 0, 0, 0, false, reported)
	if b.Limit != 64000 || b.Reserve != 2048 {
		t.Errorf("provider-reported = %+v, want 64000/2048 over table 128000/4096", b)
	}
	b = BudgetForCaps("gpt-4o-mini", 8000, 0, 0, false, reported)
	if b.Limit != 8000 || b.Reserve != 2048 {
		t.Errorf("override = %+v, want 8000 + provider reserve", b)
	}
	b = BudgetForCaps("mystery-zzz", 0, 0, 0, false, providers.Capabilities{})
	if b.Limit != 0 || b.Reserve != 4096 {
		t.Errorf("unknown = %+v, want count-mode + default reserve", b)
	}
}

// TestNegotiated_BudgetUsesReportedWindow pins the end-to-end effect:
// a small reported window bounds the provider's view by tokens.
func TestNegotiated_BudgetUsesReportedWindow(t *testing.T) {
	var history []providers.Message
	history = append(history, providers.Message{Role: providers.UserRole, Content: "goal"})
	for i := 0; i < 30; i++ {
		history = append(history, providers.Message{Role: providers.UserRole, Content: strings.Repeat("q", 400)})
		history = append(history, providers.Message{Role: providers.AssistantRole, Content: strings.Repeat("a", 400)})
	}
	inner := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{Text: "done", Done: true}},
	}}
	p := &capsScriptedProvider{
		scriptedProvider: inner,
		caps:             providers.Capabilities{Streaming: true, ToolCalling: true, ParallelToolCalls: true, ContextWindow: 2000, MaxOutputTokens: 500},
	}
	manager := tools.NewManager(tools.NewRegistry())
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
		cfg:       &config.Config{},
	}
	events, err := rt.StreamChat(context.Background(), history)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range events {
	}
	if len(inner.messages) != 1 {
		t.Fatalf("turns = %d, want 1", len(inner.messages))
	}
	// Window: system + goal + recent tail within ~1500 tokens; the full
	// 61-message history must not reach the provider.
	if got := len(inner.messages[0]); got >= len(history) {
		t.Errorf("provider saw %d messages of %d history: budget not applied", got, len(history))
	}
	foundGoal := false
	for _, m := range inner.messages[0] {
		if m.Content == "goal" {
			foundGoal = true
		}
	}
	if !foundGoal {
		t.Error("goal lost under negotiated budget")
	}
}
