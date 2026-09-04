package providers

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestOpenCodePresets(t *testing.T) {
	zen, ok := PresetByID("opencode-zen")
	if !ok {
		t.Fatal("opencode-zen preset missing")
	}
	if zen.Type != "opencode-zen" || zen.BaseURL != "https://opencode.ai/zen/v1" {
		t.Errorf("zen preset = %+v", zen)
	}
	if zen.AuthEnvVar != "OPENCODE_API_KEY" || zen.Auth != AuthRequired || zen.Scope != ScopeCloud {
		t.Errorf("zen auth/scope = %+v", zen)
	}
	if len(zen.Models) == 0 {
		t.Error("zen catalog is empty")
	}

	goP, ok := PresetByID("opencode-go")
	if !ok {
		t.Fatal("opencode-go preset missing")
	}
	if goP.Type != "opencode-go" || goP.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Errorf("go preset = %+v", goP)
	}
	if goP.AuthEnvVar != "OPENCODE_API_KEY" || goP.Auth != AuthRequired {
		t.Errorf("go auth = %+v", goP)
	}
}

func TestOpenCodeFactoriesRegistered(t *testing.T) {
	for _, typ := range []string{"openai-responses", "opencode-zen", "opencode-go"} {
		if !DefaultFactories().HasType(typ) {
			t.Errorf("factory %q not registered", typ)
		}
	}
	if !IsKnownType("opencode-zen") || !IsKnownType("opencode-go") || !IsKnownType("openai-responses") {
		t.Error("new types not recognized")
	}
}

func TestOpenCodeRouting(t *testing.T) {
	cases := []struct {
		service  string
		model    string
		protocol string
	}{
		{"zen", "gpt-5.5", "openai-responses"},
		{"zen", "grok-4.6", "openai-responses"},
		{"zen", "claude-sonnet-4-5", "anthropic"},
		{"zen", "qwen3.7-max", "anthropic"},
		{"zen", "glm-5.2", "openai-compatible"},
		{"zen", "minimax-m3", "openai-compatible"},
		{"go", "gpt-5.6-luna", "openai-responses"},
		{"go", "muse-spark-1.3-contributor", "openai-responses"},
		{"go", "minimax-m3", "anthropic"},
		{"go", "qwen3.7-plus", "anthropic"},
		{"go", "glm-5.3", "openai-compatible"},
		{"go", "deepseek-v4-flash", "openai-compatible"},
	}
	for _, tc := range cases {
		var router *OpenCodeRouter
		var err error
		spec := Spec{ID: "x", BaseURL: "http://localhost:9", Model: tc.model}
		if tc.service == "zen" {
			router, err = NewOpenCodeZen(spec)
		} else {
			router, err = NewOpenCodeGo(spec)
		}
		if err != nil {
			t.Errorf("%s/%s: %v", tc.service, tc.model, err)
			continue
		}
		if router.Protocol() != tc.protocol {
			t.Errorf("%s/%s protocol = %q, want %q", tc.service, tc.model, router.Protocol(), tc.protocol)
		}
	}
}

func TestOpenCodeMinimaxDivergence(t *testing.T) {
	// The same model ID routes differently per service. This is why
	// protocol selection is per (service, model), never per model alone.
	zen, err := NewOpenCodeZen(Spec{ID: "x", BaseURL: "http://localhost:9", Model: "minimax-m3"})
	if err != nil {
		t.Fatal(err)
	}
	goP, err := NewOpenCodeGo(Spec{ID: "x", BaseURL: "http://localhost:9", Model: "minimax-m3"})
	if err != nil {
		t.Fatal(err)
	}
	if zen.Protocol() != "openai-compatible" || goP.Protocol() != "anthropic" {
		t.Errorf("zen=%q go=%q", zen.Protocol(), goP.Protocol())
	}
}

func TestOpenCodeEmptyModelDefersFailure(t *testing.T) {
	// Capability probes construct without a model; construction succeeds
	// but any turn fails locally before touching the network.
	r, err := NewOpenCodeZen(Spec{ID: "x", BaseURL: "http://localhost:9", Model: ""})
	if err != nil {
		t.Fatalf("empty model must construct: %v", err)
	}
	if _, err := r.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil); err == nil {
		t.Fatal("empty-model turn must fail locally")
	}
}

func TestOpenCodeUnknownModelErrors(t *testing.T) {
	_, err := NewOpenCodeZen(Spec{ID: "x", BaseURL: "http://localhost:9", Model: "gpt-99"})
	if err == nil {
		t.Fatal("unknown model must fail deterministically, not guess a protocol")
	}
	if !strings.Contains(err.Error(), "gpt-99") {
		t.Errorf("error should name the model: %v", err)
	}
}

func TestOpenCodeRouterDelegatesTurn(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want responses transport for gpt-5.5", r.URL.Path)
		}
		writeResponsesSSE(w,
			`{"type":"response.output_text.delta","delta":"hi"}`,
			`{"type":"response.completed","response":{"status":"completed"}}`,
		)
	})
	spec := Spec{ID: "opencode-zen", Type: "opencode-zen", Label: "OpenCode Zen", BaseURL: url, Model: "gpt-5.5", APIKey: "k"}
	p, err := DefaultFactories().Create(spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	router, ok := p.(*OpenCodeRouter)
	if !ok {
		t.Fatalf("created %T, want *OpenCodeRouter", p)
	}
	stream, err := router.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, done := streamTextAndDone(t, stream)
	if text != "hi" || !done {
		t.Errorf("text=%q done=%v", text, done)
	}
}

func TestOpenCodeRouterChatPath(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want chat transport for glm-5.2", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"yo\"},\"finish_reason\":\"stop\"}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	})
	spec := Spec{ID: "opencode-go", Type: "opencode-go", Label: "OpenCode Go", BaseURL: url, Model: "glm-5.2", APIKey: "k"}
	p, err := DefaultFactories().Create(spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, done := streamTextAndDone(t, stream)
	if text != "yo" || !done {
		t.Errorf("text=%q done=%v", text, done)
	}
}

func TestOpenCodeRouterCapabilities(t *testing.T) {
	r, err := NewOpenCodeGo(Spec{ID: "x", BaseURL: "http://localhost:9", Model: "glm-5.2"})
	if err != nil {
		t.Fatal(err)
	}
	caps := r.Capabilities()
	if !caps.Streaming || !caps.ToolCalling || !caps.Reasoning || !caps.ParallelToolCalls {
		t.Errorf("caps = %+v", caps)
	}
}

func TestOpenCodeReasoningCapabilities(t *testing.T) {
	if caps := ModelReasoningCapabilities("opencode-zen", "gpt-5.5"); !caps.SupportsEffort() {
		t.Errorf("zen responses model should support effort: %+v", caps)
	}
	if caps := ModelReasoningCapabilities("opencode-zen", "claude-opus-4-5"); !caps.SupportsThinking() {
		t.Errorf("zen claude model should support thinking: %+v", caps)
	}
	// Unverified transports stay conservative.
	if caps := ModelReasoningCapabilities("opencode-go", "glm-5.2"); caps.SupportsEffort() || caps.SupportsThinking() {
		t.Errorf("unverified chat model must not advertise reasoning: %+v", caps)
	}
	if caps := ModelReasoningCapabilities("opencode-go", "qwen3.7-max"); caps.SupportsThinking() {
		t.Errorf("unverified messages model must not advertise thinking: %+v", caps)
	}
	if caps := ModelReasoningCapabilities("opencode-zen", "gpt-99"); caps.SupportsEffort() || caps.SupportsThinking() {
		t.Errorf("unknown model must not advertise reasoning: %+v", caps)
	}
}

func TestOpenCodeCatalogProtocolMatchesRouter(t *testing.T) {
	for _, presetID := range []string{"opencode-zen", "opencode-go"} {
		preset, ok := PresetByID(presetID)
		if !ok {
			t.Fatalf("preset %q missing", presetID)
		}
		for _, m := range preset.Models {
			if m.Protocol == "" {
				t.Errorf("%s/%s has no protocol", presetID, m.ID)
				continue
			}
			var router *OpenCodeRouter
			var err error
			spec := Spec{ID: "x", BaseURL: "http://localhost:9", Model: m.ID}
			if presetID == "opencode-zen" {
				router, err = NewOpenCodeZen(spec)
			} else {
				router, err = NewOpenCodeGo(spec)
			}
			if err != nil {
				t.Errorf("%s/%s: router rejects catalog model: %v", presetID, m.ID, err)
				continue
			}
			if router.Protocol() != m.Protocol {
				t.Errorf("%s/%s catalog protocol %q != router %q", presetID, m.ID, m.Protocol, router.Protocol())
			}
		}
	}
}

func TestOpenCodeListModels(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"data":[{"id":"glm-5.2"},{"id":"kimi-k3"}]}`))
	})
	r, err := NewOpenCodeGo(Spec{ID: "x", BaseURL: url, Model: "glm-5.2", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := r.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "glm-5.2" {
		t.Errorf("models = %+v", models)
	}
}

func TestOpenCodeSetReasoningDelegates(t *testing.T) {
	r, err := NewOpenCodeZen(Spec{ID: "x", BaseURL: "http://localhost:9", Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	r.SetReasoning(ReasoningConfig{Effort: "high"})
	if got := r.GetReasoning(); got.Effort != "high" {
		t.Errorf("effort = %q", got.Effort)
	}
	inner, ok := r.inner.(ReasoningAware)
	if !ok {
		t.Fatal("inner adapter is not ReasoningAware")
	}
	if got := inner.GetReasoning(); got.Effort != "high" {
		t.Errorf("inner effort = %q", got.Effort)
	}
}
