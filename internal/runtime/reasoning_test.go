package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

func newTestRuntimeWithProvider(p providers.ModelProvider) *Runtime {
	manager := tools.NewManager(tools.NewRegistry())
	return &Runtime{
		provider:            p,
		agent:               agent.New("test", "system", ""),
		manager:             manager,
		scheduler:           newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}
}

func TestSetEffortValidAndInvalid(t *testing.T) {
	// Use nvidia provider which supports effort for z-ai/glm-5.2
	// Create a temp config home to avoid polluting real config
	// For this unit test we construct runtime directly with provider
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	spec := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: server.URL, Model: "z-ai/glm-5.2"}
	p, _ := providers.DefaultFactories().Create(spec)
	rt := &Runtime{
		cfg: &config.Config{
			Model: config.Model{Provider: "nvidia", Name: "z-ai/glm-5.2"},
		},
		provider:            p,
		agent:               agent.New("test", "", ""),
		manager:             tools.NewManager(tools.NewRegistry()),
		discovery:           providers.NewDiscovery(providers.DefaultFactories()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}

	// Valid level
	if err := rt.SetEffort("high"); err != nil {
		t.Fatalf("SetEffort high = %v", err)
	}
	if got := rt.CurrentEffort(); got != "high" {
		t.Errorf("CurrentEffort = %q, want high", got)
	}
	// Invalid level should not mutate
	if err := rt.SetEffort("ultra"); err == nil {
		t.Fatal("SetEffort ultra succeeded")
	}
	if got := rt.CurrentEffort(); got != "high" {
		t.Errorf("CurrentEffort after invalid = %q, want still high", got)
	}
	// Ensure provider got correct field
	var gotBody map[string]any
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server2.Close()
	spec2 := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: server2.URL, Model: "z-ai/glm-5.2"}
	p2, _ := providers.DefaultFactories().Create(spec2)
	rt.provider = p2
	rt.applyReasoning()
	stream, _ := p2.StreamChat(context.Background(), nil, nil)
	for range stream {
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", gotBody["reasoning_effort"])
	}
}

func TestThinkingBoolToggleAndInvalid(t *testing.T) {
	// Ollama provider supports thinking bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{\"message\":{\"content\":\"hi\"},\"done\":true}\n"))
	}))
	defer server.Close()
	p := providers.NewOllamaProvider(server.URL, "ornith:9b")
	rt := &Runtime{
		cfg:                 &config.Config{Model: config.Model{Provider: "ollama", Name: "ornith:9b"}},
		provider:            p,
		agent:               agent.New("test", "", ""),
		manager:             tools.NewManager(tools.NewRegistry()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}
	// Toggle on
	enabled, err := rt.ToggleThinking()
	if err != nil {
		t.Fatalf("ToggleThinking = %v", err)
	}
	if !enabled {
		t.Errorf("ToggleThinking first = %v, want true", enabled)
	}
	tc := rt.CurrentThinking()
	if tc == nil || tc.Enabled == nil || !*tc.Enabled {
		t.Fatalf("CurrentThinking after toggle = %v, want on", tc)
	}
	// Set explicit off
	off := false
	if err := rt.SetThinking(providers.ThinkingConfig{Enabled: &off}); err != nil {
		t.Fatalf("SetThinking off = %v", err)
	}
	tc = rt.CurrentThinking()
	if tc.Enabled == nil || *tc.Enabled {
		t.Errorf("after off, enabled = %v, want false", tc.Enabled)
	}
	// Invalid: try to set budget for bool provider
	budget := 1024
	if err := rt.SetThinking(providers.ThinkingConfig{Budget: &budget}); err == nil {
		t.Fatal("SetThinking budget for bool provider succeeded")
	}
	// Ensure still off
	tc = rt.CurrentThinking()
	if tc.Enabled == nil || *tc.Enabled {
		t.Errorf("after invalid budget, enabled = %v, want still false", tc.Enabled)
	}
}

func TestThinkingBudgetValidAndInvalid(t *testing.T) {
	server := newTestAnthropicServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	})
	p := providers.NewAnthropicProvider(providers.Spec{ID: "anthropic", Type: "anthropic", BaseURL: server, Model: "claude-sonnet-4-5"})
	rt := &Runtime{
		cfg:                 &config.Config{Model: config.Model{Provider: "anthropic", Name: "claude-sonnet-4-5"}},
		provider:            p,
		agent:               agent.New("test", "", ""),
		manager:             tools.NewManager(tools.NewRegistry()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}
	b := 4096
	if err := rt.SetThinking(providers.ThinkingConfig{Budget: &b}); err != nil {
		t.Fatalf("SetThinking budget 4096 = %v", err)
	}
	tc := rt.CurrentThinking()
	if tc.Budget == nil || *tc.Budget != 4096 {
		t.Fatalf("CurrentThinking budget = %v, want 4096", tc)
	}
	// Invalid budget below min
	b = 100
	if err := rt.SetThinking(providers.ThinkingConfig{Budget: &b}); err == nil {
		t.Fatal("SetThinking budget 100 succeeded, want error")
	}
	// Still 4096
	tc = rt.CurrentThinking()
	if tc == nil {
		t.Fatalf("CurrentThinking nil after invalid, want 4096; caps=%+v", rt.CurrentReasoningCapabilities())
	}
	if tc.Budget == nil || *tc.Budget != 4096 {
		t.Errorf("after invalid, budget = %v, want 4096", tc.Budget)
	}
}

func newTestAnthropicServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestSwitchingModelsDoesNotLeakEffort(t *testing.T) {
	// Model A (nvidia z-ai/glm-5.2) supports effort high, model B (anthropic claude-3-opus unsupported) should not receive it.
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	specA := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: serverA.URL, Model: "z-ai/glm-5.2"}
	pA, _ := providers.DefaultFactories().Create(specA)
	specB := providers.Spec{ID: "anthropic", Type: "anthropic", BaseURL: serverB.URL, Model: "claude-3-opus"}
	pB, _ := providers.DefaultFactories().Create(specB)

	rt := &Runtime{
		cfg:                 &config.Config{Model: config.Model{Provider: "nvidia", Name: "z-ai/glm-5.2"}},
		provider:            pA,
		agent:               agent.New("test", "", ""),
		manager:             tools.NewManager(tools.NewRegistry()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}
	if err := rt.SetEffort("high"); err != nil {
		t.Fatalf("SetEffort high = %v", err)
	}
	// Verify A sends effort
	var gotBody map[string]any
	serverA2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer serverA2.Close()
	specA2 := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: serverA2.URL, Model: "z-ai/glm-5.2"}
	pA2, _ := providers.DefaultFactories().Create(specA2)
	rt.provider = pA2
	rt.applyReasoning()
	stream, _ := pA2.StreamChat(context.Background(), nil, nil)
	for range stream {
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("A reasoning_effort = %v, want high", gotBody["reasoning_effort"])
	}

	// Switch to B (unsupported)
	rt.cfg.Model.Provider = "anthropic"
	rt.cfg.Model.Name = "claude-3-opus"
	rt.provider = pB
	rt.applyReasoning()
	var gotBodyB map[string]any
	// Use a server to capture B's request
	serverB2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBodyB)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer serverB2.Close()
	specB2 := providers.Spec{ID: "anthropic", Type: "anthropic", BaseURL: serverB2.URL, Model: "claude-3-opus"}
	pB2, _ := providers.DefaultFactories().Create(specB2)
	rt.provider = pB2
	rt.applyReasoning()
	stream, _ = pB2.StreamChat(context.Background(), nil, nil)
	for range stream {
	}
	if gotBodyB != nil {
		if _, has := gotBodyB["reasoning_effort"]; has {
			t.Errorf("B sent reasoning_effort %v for unsupported model", gotBodyB["reasoning_effort"])
		}
		if _, has := gotBodyB["thinking"]; has {
			// B is anthropic claude-3-opus unsupported, should not have thinking
			t.Errorf("B sent thinking for unsupported model: %v", gotBodyB["thinking"])
		}
	}
	// Switch back to A, should restore high
	rt.cfg.Model.Provider = "nvidia"
	rt.cfg.Model.Name = "z-ai/glm-5.2"
	rt.provider = pA2
	rt.applyReasoning()
	if got := rt.CurrentEffort(); got != "high" {
		t.Errorf("after switch back, CurrentEffort = %q, want high", got)
	}
}

func TestUnsupportedModelSetEffortFails(t *testing.T) {
	rt := &Runtime{
		cfg:                 &config.Config{Model: config.Model{Provider: "ollama", Name: "ornith:9b"}},
		agent:               agent.New("test", "", ""),
		manager:             tools.NewManager(tools.NewRegistry()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}
	// Ollama does not support effort
	if err := rt.SetEffort("high"); err == nil {
		t.Fatal("SetEffort for ollama should fail")
	}
	if got := rt.CurrentEffort(); got != "" {
		t.Errorf("CurrentEffort = %q, want empty for unsupported", got)
	}
}

func TestApplyReasoningFiltersStale(t *testing.T) {
	// Store effort for nvidia model, then switch provider to ollama (effort unsupported)
	// Ensure ollama request has no reasoning_effort, and nvidia still retains.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{\"message\":{\"content\":\"hi\"},\"done\":true}\n"))
	}))
	defer server.Close()
	pOllama := providers.NewOllamaProvider(server.URL, "ornith:9b")
	rt := &Runtime{
		cfg:                 &config.Config{Model: config.Model{Provider: "nvidia", Name: "z-ai/glm-5.2"}},
		provider:            pOllama, // will be overridden
		agent:               agent.New("test", "", ""),
		manager:             tools.NewManager(tools.NewRegistry()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}
	// Set effort for nvidia
	if err := rt.SetEffort("xhigh"); err != nil {
		t.Fatalf("SetEffort xhigh = %v", err)
	}
	// Switch to ollama
	rt.cfg.Model.Provider = "ollama"
	rt.cfg.Model.Name = "ornith:9b"
	rt.provider = pOllama
	rt.applyReasoning()
	// Ollama provider's reasoning should be empty effort
	if pOllama.GetReasoning().Effort != "" {
		t.Errorf("ollama reasoning effort = %q, want empty after switch to unsupported", pOllama.GetReasoning().Effort)
	}
	// Switch back to nvidia, should restore xhigh
	rt.cfg.Model.Provider = "nvidia"
	rt.cfg.Model.Name = "z-ai/glm-5.2"
	// Need a new nvidia provider with that model
	spec := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: "http://example.com", Model: "z-ai/glm-5.2"}
	pNvidia, _ := providers.DefaultFactories().Create(spec)
	rt.provider = pNvidia
	rt.applyReasoning()
	if pNvidia.(providers.ReasoningAware).GetReasoning().Effort != "xhigh" {
		t.Errorf("nvidia restored effort = %q, want xhigh", pNvidia.(providers.ReasoningAware).GetReasoning().Effort)
	}
}

func TestSwitching_NvidiaNewModels_DoesNotLeak(t *testing.T) {
	// Sequence from task: unsupported → DeepSeek (max) → Muse (minimal) → unsupported
	// Verify no leak and exact API levels.
	rt := &Runtime{
		cfg:                 &config.Config{Model: config.Model{Provider: "nvidia", Name: "google/codgemma-7b"}},
		agent:               agent.New("test", "", ""),
		manager:             tools.NewManager(tools.NewRegistry()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}
	// Start unsupported
	if caps := rt.CurrentReasoningCapabilities(); caps.SupportsEffort() {
		t.Fatal("google/codgemma-7b should be unsupported")
	}
	// Switch to DeepSeek
	rt.cfg.Model.Name = "deepseek-ai/deepseek-v4-flash-0731"
	specDeep := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: "http://example.com", Model: "deepseek-ai/deepseek-v4-flash-0731"}
	pDeep, _ := providers.DefaultFactories().Create(specDeep)
	rt.provider = pDeep
	rt.applyReasoning()
	if err := rt.SetEffort("max"); err != nil {
		t.Fatalf("DeepSeek SetEffort max = %v", err)
	}
	if got := rt.CurrentEffort(); got != "max" {
		t.Fatalf("DeepSeek CurrentEffort = %q, want max", got)
	}
	// Verify request has reasoning_effort max, no thinking, no duplicate
	var gotBodyDeep map[string]any
	srvDeep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBodyDeep)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	specDeep2 := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: srvDeep.URL, Model: "deepseek-ai/deepseek-v4-flash-0731"}
	pDeep2, _ := providers.DefaultFactories().Create(specDeep2)
	rt.provider = pDeep2
	rt.applyReasoning()
	ch, _ := pDeep2.StreamChat(context.Background(), nil, nil)
	for range ch {
	}
	srvDeep.Close()
	if gotBodyDeep["reasoning_effort"] != "max" {
		t.Errorf("DeepSeek request reasoning_effort = %v, want max", gotBodyDeep["reasoning_effort"])
	}
	if _, has := gotBodyDeep["thinking"]; has {
		t.Errorf("DeepSeek should not have thinking field")
	}
	if kwargs, ok := gotBodyDeep["chat_template_kwargs"].(map[string]any); ok {
		if _, has := kwargs["reasoning_effort"]; has {
			t.Errorf("DeepSeek should not duplicate reasoning_effort inside kwargs, got %v", kwargs)
		}
	}

	// Switch to Muse Glimmer
	rt.cfg.Model.Name = "meta/muse-glimmer-30b"
	specMuse := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: "http://example.com", Model: "meta/muse-glimmer-30b"}
	pMuse, _ := providers.DefaultFactories().Create(specMuse)
	rt.provider = pMuse
	rt.applyReasoning()
	// Initially no effort for Muse, should be empty
	if got := rt.CurrentEffort(); got != "" {
		t.Fatalf("Muse initial effort = %q, want empty (not yet set)", got)
	}
	if err := rt.SetEffort("minimal"); err != nil {
		t.Fatalf("Muse SetEffort minimal = %v", err)
	}
	if got := rt.CurrentEffort(); got != "minimal" {
		t.Fatalf("Muse CurrentEffort = %q, want minimal", got)
	}
	// Verify xhigh is invalid for Muse (API uses max, not xhigh)
	if err := rt.SetEffort("xhigh"); err == nil {
		t.Fatal("Muse xhigh should be invalid")
	}
	// Check request for Muse minimal
	var gotBodyMuse map[string]any
	srvMuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBodyMuse)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	specMuse2 := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: srvMuse.URL, Model: "meta/muse-glimmer-30b"}
	pMuse2, _ := providers.DefaultFactories().Create(specMuse2)
	rt.provider = pMuse2
	rt.applyReasoning()
	ch, _ = pMuse2.StreamChat(context.Background(), nil, nil)
	for range ch {
	}
	srvMuse.Close()
	if gotBodyMuse["reasoning_effort"] != "minimal" {
		t.Errorf("Muse request reasoning_effort = %v, want minimal", gotBodyMuse["reasoning_effort"])
	}

	// Switch back to unsupported, should have no effort
	rt.cfg.Model.Name = "google/codgemma-7b"
	specUns := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: "http://example.com", Model: "google/codgemma-7b"}
	pUns, _ := providers.DefaultFactories().Create(specUns)
	rt.provider = pUns
	rt.applyReasoning()
	if got := rt.CurrentEffort(); got != "" {
		t.Errorf("unsupported after roundtrip CurrentEffort = %q, want empty", got)
	}
	var gotBodyUns map[string]any
	srvUns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBodyUns)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	specUns2 := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: srvUns.URL, Model: "google/codgemma-7b"}
	pUns2, _ := providers.DefaultFactories().Create(specUns2)
	rt.provider = pUns2
	rt.applyReasoning()
	ch, _ = pUns2.StreamChat(context.Background(), nil, nil)
	for range ch {
	}
	srvUns.Close()
	if _, has := gotBodyUns["reasoning_effort"]; has {
		t.Errorf("unsupported should not send reasoning_effort, got %v", gotBodyUns["reasoning_effort"])
	}
	// Verify DeepSeek and Muse stored values still there
	rt.cfg.Model.Name = "deepseek-ai/deepseek-v4-flash-0731"
	pDeep, _ = providers.DefaultFactories().Create(specDeep)
	rt.provider = pDeep
	rt.applyReasoning()
	if got := rt.CurrentEffort(); got != "max" {
		t.Errorf("DeepSeek restored after roundtrip = %q, want max", got)
	}
	rt.cfg.Model.Name = "meta/muse-glimmer-30b"
	pMuse, _ = providers.DefaultFactories().Create(specMuse)
	rt.provider = pMuse
	rt.applyReasoning()
	if got := rt.CurrentEffort(); got != "minimal" {
		t.Errorf("Muse restored after roundtrip = %q, want minimal", got)
	}
}

func TestDeepSeek_NoneIsEffortNotThinking(t *testing.T) {
	rt := &Runtime{
		cfg:                 &config.Config{Model: config.Model{Provider: "nvidia", Name: "deepseek-ai/deepseek-v4-flash-0731"}},
		agent:               agent.New("test", "", ""),
		manager:             tools.NewManager(tools.NewRegistry()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}
	spec := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: "http://example.com", Model: "deepseek-ai/deepseek-v4-flash-0731"}
	p, _ := providers.DefaultFactories().Create(spec)
	rt.provider = p
	if err := rt.SetEffort("none"); err != nil {
		t.Fatalf("DeepSeek SetEffort none = %v", err)
	}
	if got := rt.CurrentEffort(); got != "none" {
		t.Fatalf("none effort = %q", got)
	}
	// Thinking should be unsupported
	if caps := rt.CurrentReasoningCapabilities(); caps.SupportsThinking() {
		t.Fatal("DeepSeek thinking should be unsupported")
	}
	if tc := rt.CurrentThinking(); tc != nil {
		t.Fatalf("CurrentThinking = %v, want nil for DeepSeek", tc)
	}
	// Verify request has reasoning_effort none, no thinking
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	spec2 := providers.Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: srv.URL, Model: "deepseek-ai/deepseek-v4-flash-0731"}
	p2, _ := providers.DefaultFactories().Create(spec2)
	rt.provider = p2
	rt.applyReasoning()
	ch, _ := p2.StreamChat(context.Background(), nil, nil)
	for range ch {
	}
	srv.Close()
	if gotBody["reasoning_effort"] != "none" {
		t.Errorf("DeepSeek none request = %v, want none", gotBody["reasoning_effort"])
	}
	if _, has := gotBody["thinking"]; has {
		t.Errorf("DeepSeek none should not have thinking field")
	}
}
