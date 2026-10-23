package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

func intPtr(i int) *int { return &i }

// concurrencyProbeProvider is a scriptedProvider that also records the
// peak number of concurrently in-flight StreamChat calls. Buffered
// events would make the window tiny, so each call holds the slot long
// enough for true overlap to be observable.
type concurrencyProbeProvider struct {
	turns    [][]providers.StreamEvent
	mu       sync.Mutex
	calls    int
	inflight int
	peak     int
}

func (p *concurrencyProbeProvider) StreamChat(_ context.Context, messages []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	p.inflight++
	if p.inflight > p.peak {
		p.peak = p.inflight
	}
	callIndex := p.calls - 1
	if callIndex >= len(p.turns) {
		p.mu.Unlock()
		return nil, context.Canceled // out of script: fail fast, never stall
	}
	turn := p.turns[callIndex]
	p.mu.Unlock()

	defer func() {
		time.Sleep(10 * time.Millisecond) // widen the in-flight window
		p.mu.Lock()
		p.inflight--
		p.mu.Unlock()
	}()

	events := make(chan providers.StreamEvent, len(turn))
	for _, event := range turn {
		events <- event
	}
	close(events)
	return events, nil
}

func TestDelegateStrippedFromChildAtDepthLimit(t *testing.T) {
	// maxDepth=1: the spawned child is the structural leaf. Its universe
	// never contains delegate, so a grandchild attempt fails at tool
	// resolution with no additional child run spawned.
	full := tools.NewManager(tools.NewRegistry())
	if err := full.Register(echoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	parent := &scriptedProvider{turns: [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "researcher",
					"task":  "try to delegate further",
				},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-grandchild",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "researcher",
					"task":  "grandchild attempt",
				},
			}}},
			{Done: true},
		},
		{
			{Text: "leaf done"},
			{Done: true},
		},
		{
			{Text: "coordinated."},
			{Done: true},
		},
	}}

	registry := agent.NewRegistry()
	if err := registry.Register(agent.Definition{
		Name:         "researcher",
		Description:  "test child that explicitly names delegate",
		SystemPrompt: "child-system",
		Tools:        []string{"echo", "delegate"},
		Skills:       []string{},
	}); err != nil {
		t.Fatalf("register researcher: %v", err)
	}
	orch := newOrchestrator(config.Orchestration{Enabled: true, MaxDepth: intPtr(1)}, registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
	}
	runtime := &Runtime{
		provider:    parent,
		agent:       agent.New("root", "root-system", ""),
		activeAgent: "root",
		manager:     full,
		fullManager: full,
		orch:        orch,
		scheduler:   newScheduler(full, nil, nil, DefaultSchedulerConfig),
	}
	orch.parent = runtime

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "coordinate"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	delegateFinish, final := delegateRunSummary(events)
	if final == nil || final.Content != "coordinated." {
		t.Fatalf("final response = %#v, want the parent's closing answer", final)
	}
	if delegateFinish == nil {
		t.Fatalf("delegate call never produced a terminal result")
	}
	if !strings.Contains(delegateFinish.Content, "failed_tool_calls=1") {
		t.Fatalf("delegate result = %q, want the child's delegate attempt failed", delegateFinish.Content)
	}
	if parent.calls != 4 {
		t.Fatalf("provider calls = %d, want 4 (no grandchild run spawned)", parent.calls)
	}
}

func TestNestedDelegationAllowedWithinDepth(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	if err := full.Register(echoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	registry := agent.NewRegistry()
	for _, def := range []agent.Definition{
		{Name: "researcher", Description: "mid-level child", SystemPrompt: "child-system", Tools: []string{"echo", "delegate"}, Skills: []string{}},
		{Name: "gchild", Description: "leaf grandchild", SystemPrompt: "gchild-system", Tools: []string{"echo"}, Skills: []string{}},
	} {
		if err := registry.Register(def); err != nil {
			t.Fatalf("register %s: %v", def.Name, err)
		}
	}
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "researcher",
					"task":  "mid-level work",
				},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-sub-delegate",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "gchild",
					"task":  "leaf work",
				},
			}}},
			{Done: true},
		},
		{
			{Text: "grandchild report"},
			{Done: true},
		},
		{
			{Text: "researcher report"},
			{Done: true},
		},
		{
			{Text: "coordinated."},
			{Done: true},
		},
	}}

	orch := newOrchestrator(config.Orchestration{Enabled: true, MaxDepth: intPtr(2)}, registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
	}
	runtime := &Runtime{
		provider:    provider,
		agent:       agent.New("root", "root-system", ""),
		activeAgent: "root",
		manager:     full,
		fullManager: full,
		orch:        orch,
		scheduler:   newScheduler(full, nil, nil, DefaultSchedulerConfig),
	}
	orch.parent = runtime

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "coordinate"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	delegateFinish, final := delegateRunSummary(events)
	if final == nil || final.Content != "coordinated." {
		t.Fatalf("final response = %#v, want the parent's closing answer", final)
	}
	if delegateFinish == nil {
		t.Fatalf("delegate call never produced a terminal result")
	}
	// The parent sees the researcher's own report: the grandchild's
	// report went to its parent (the researcher) as a tool result. What
	// is provable at the outer level is that the researcher's nested
	// delegate call ran (tool_calls=1) and consumed its turn.
	if !strings.Contains(delegateFinish.Content, "researcher report") {
		t.Fatalf("delegate result = %q, want the researcher's own report", delegateFinish.Content)
	}
	if !strings.Contains(delegateFinish.Content, "tool_calls=1") {
		t.Fatalf("delegate result = %q, want the nested delegate call recorded", delegateFinish.Content)
	}
	if provider.calls != 5 {
		t.Fatalf("provider calls = %d, want 5 (p, c-delegate, g, c-final, p-final)", provider.calls)
	}
}

func childConfig(t testing.TB, concurrency, perRun int) config.Orchestration {
	t.Helper()
	return config.Orchestration{
		Enabled:               true,
		MaxConcurrentChildren: intPtr(concurrency),
		MaxChildrenPerRun:     intPtr(perRun),
	}
}

func newProbeRuntime(t *testing.T, p providers.ModelProvider, orchCfg config.Orchestration, registry *agent.Registry) *Runtime {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	if err := full.Register(echoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	orch := newOrchestrator(orchCfg, registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
	}
	runtime := &Runtime{
		provider:    p,
		agent:       agent.New("root", "root-system", ""),
		activeAgent: "root",
		manager:     full,
		fullManager: full,
		orch:        orch,
		scheduler:   newScheduler(full, nil, nil, DefaultSchedulerConfig),
	}
	orch.parent = runtime
	return runtime
}

func TestMaxConcurrentChildrenEnforced(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := &concurrencyProbeProvider{turns: [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{
				{ID: "call-d1", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "a"}},
				{ID: "call-d2", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "b"}},
			}},
			{Done: true},
		},
		{{Text: "child one done"}, {Done: true}},
		{{Text: "child two done"}, {Done: true}},
		{{Text: "coordinated."}, {Done: true}},
	}}
	runtime := newProbeRuntime(t, provider, childConfig(t, 1, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "fan out"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var finishes int
	for e := range events {
		if e.Type == EventToolFinish && e.ToolResult != nil && e.ToolResult.Name == "delegate" {
			finishes++
		}
	}
	if finishes != 2 {
		t.Fatalf("delegate finishes = %d, want 2", finishes)
	}
	if provider.calls != 4 {
		t.Fatalf("provider calls = %d, want 4", provider.calls)
	}
	if p := provider.peak; p > 1 {
		t.Fatalf("peak concurrent child provider turns = %d, want exactly 1 under a cap of 1", p)
	}
}

func TestMaxChildrenPerRunCumulative(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{
				{ID: "call-d1", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "a"}},
				{ID: "call-d2", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "b"}},
				{ID: "call-d3", Name: "delegate", Arguments: map[string]any{"agent": "researcher", "task": "c"}},
			}},
			{Done: true},
		},
		{{Text: "child one done"}, {Done: true}},
		{{Text: "child two done"}, {Done: true}},
		{{Text: "coordinated."}, {Done: true}},
	}}
	// maxConcurrentChildren=1 makes the admit order deterministic enough:
	// exactly two spawned children, the third one rejected.
	runtime := newProbeRuntime(t, provider, childConfig(t, 1, 2), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "fan out"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var successes, rejected int
	var limitTexts []string
	for e := range events {
		if e.ToolResult != nil && e.ToolResult.Name == "delegate" {
			if e.Type == EventToolFinish {
				successes++
			}
			if e.Type == EventToolFailed && strings.Contains(e.ToolResult.Content, "delegation limit reached") {
				rejected++
				limitTexts = append(limitTexts, e.ToolResult.Content)
			}
		}
	}
	if successes != 2 || rejected != 1 {
		t.Fatalf("delegate results = (%d success, %d rejected), want (2, 1); texts: %v", successes, rejected, limitTexts)
	}
	if len(limitTexts) == 0 || !strings.Contains(limitTexts[0], "2 child runs") {
		t.Fatalf("limit message = %v, want an honest cumulative count", limitTexts)
	}
	if provider.calls != 4 {
		t.Fatalf("provider calls = %d, want 4 (no third child spawned)", provider.calls)
	}
}
