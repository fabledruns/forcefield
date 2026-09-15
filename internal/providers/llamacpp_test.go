package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// llamaCppServer spins up a minimal llama-server stand-in: an
// OpenAI-compatible HTTP API serving /chat/completions (SSE) and /models
// with no authentication. The handler sees every request.
func llamaCppServer(t *testing.T, handle func(t *testing.T, w http.ResponseWriter, r *http.Request)) (*httptest.Server, *OpenAICompatible) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(t, w, r)
	}))
	t.Cleanup(server.Close)
	p := NewOpenAICompatible(Spec{
		ID:      "llama-cpp",
		Type:    "openai-compatible",
		Label:   "llama.cpp",
		BaseURL: server.URL,
		Model:   "my-model",
	})
	return server, p
}

// TestLlamaCppPresetSharesOpenAICompatibleTransport pins the integration
// architecture: llama.cpp is a catalog preset on the shared transport,
// not a separate adapter. The factory must build the same
// OpenAICompatible implementation every other compatible service uses.
func TestLlamaCppPresetSharesOpenAICompatibleTransport(t *testing.T) {
	preset, ok := PresetByID("llama-cpp")
	if !ok {
		t.Fatal("PresetByID(llama-cpp) missing from catalog")
	}
	if preset.Type != "openai-compatible" {
		t.Errorf("type = %q, want the shared OpenAI-compatible transport", preset.Type)
	}
	if preset.BaseURL != "http://localhost:8080/v1" {
		t.Errorf("base URL = %q, want llama-server's default http://localhost:8080/v1", preset.BaseURL)
	}
	if preset.Auth != AuthNone || preset.Scope != ScopeLocal {
		t.Errorf("auth/scope = %v/%q, want unauthenticated local", preset.Auth, preset.Scope)
	}
	if len(preset.Models) != 0 {
		t.Errorf("models = %v, want no fallback models (the served ID is user-defined via --alias)", preset.Models)
	}

	p, err := DefaultFactories().Create(Spec{
		ID: "llama-cpp", Type: "openai-compatible", Label: "llama.cpp",
		BaseURL: "http://localhost:8080/v1", Model: "my-model",
	})
	if err != nil {
		t.Fatalf("Create(llama-cpp) error = %v", err)
	}
	if _, ok := p.(*OpenAICompatible); !ok {
		t.Fatalf("created %T, want *OpenAICompatible (no provider-specific adapter)", p)
	}
	if !IsKnownType("llama-cpp") {
		t.Error("IsKnownType(llama-cpp) = false, want true so it is accepted as a providers entry type")
	}
}

// TestLlamaCppStreamsWithoutAuthorizationHeader covers the defining
// llama-server difference from cloud OpenAI-compatible services: requests
// carry no credentials. It also pins that streamed reasoning arrives as
// Thinking events on the shared transport without any llama.cpp-specific
// parsing code.
func TestLlamaCppStreamsWithoutAuthorizationHeader(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	_, p := llamaCppServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		} else if gotModel, _ = body["model"].(string); gotModel == "" {
			t.Error("request body has no model field")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_content":"checking the file"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"All done."},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	events := drainEvents(t, stream)

	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want no credentials for the unauthenticated local server", gotAuth)
	}
	if gotModel != "my-model" {
		t.Errorf("model = %q, want the active model passed through", gotModel)
	}

	var summary []string
	for _, e := range events {
		switch {
		case e.Err != nil:
			t.Fatalf("stream error: %v", e.Err)
		case e.Thinking != "":
			summary = append(summary, "thinking:"+e.Thinking)
		case e.Text != "":
			summary = append(summary, "text:"+e.Text)
		case e.Done:
			summary = append(summary, fmt.Sprintf("done(%s)", e.StopReason))
		}
	}
	if strings.Join(summary, ",") != "thinking:checking the file,text:All done.,done(stop)" {
		t.Fatalf("events = %v, want reasoning then text then done(stop)", summary)
	}
}

// TestLlamaCppDiscoveryListsServedModels covers model discovery against a
// llama-server-shaped /v1/models payload: the served ID is whatever the
// user set via --alias (extra metadata fields ignored), blank IDs
// dropped, and no credentials sent.
func TestLlamaCppDiscoveryListsServedModels(t *testing.T) {
	var sawAuth, sawPath string
	_, p := llamaCppServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawPath = r.URL.Path
		fmt.Fprint(w, `{"object":"list","data":[{"id":"my-alias","object":"model","created":1739296650,"owned_by":"llamacpp"},{"id":""}]}`)
	})

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if sawPath != "/models" {
		t.Errorf("path = %q, want /models", sawPath)
	}
	if sawAuth != "" {
		t.Errorf("auth = %q, want no bearer token for the unauthenticated local server", sawAuth)
	}
	if len(models) != 1 || models[0].ID != "my-alias" {
		t.Errorf("models = %#v, want the single served alias (blank dropped)", models)
	}

	// Discovery must also work end to end through the generic service, so
	// the /model picker populates without provider-specific code.
	d := NewDiscovery(DefaultFactories())
	fetched, err := d.Fetch(context.Background(), Spec{
		ID: "llama-cpp", Type: "openai-compatible", Label: "llama.cpp",
		BaseURL: p.spec.BaseURL, Model: "my-model",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(fetched) != 1 || fetched[0].ID != "my-alias" || fetched[0].Provider != "llama-cpp" {
		t.Errorf("fetched = %+v, want the served alias attributed to llama-cpp", fetched)
	}
}

// TestLlamaCppCapabilitiesAreConservative pins the approved honesty
// policy: the transport surfaces streamed reasoning, but no configurable
// effort/thinking knobs or context-window numbers are claimed for
// llama.cpp models without protocol-level evidence.
func TestLlamaCppCapabilitiesAreConservative(t *testing.T) {
	if caps := ModelReasoningCapabilities("llama-cpp", "my-model"); caps.SupportsEffort() || caps.SupportsThinking() {
		t.Errorf("reasoning capabilities = %+v, want none (streamed thinking still surfaces via the transport)", caps)
	}
	if window, _ := ContextLimitsForModel("my-model"); window != 0 {
		t.Errorf("context window = %d, want 0 (unknown, never guessed)", window)
	}
	transport := CapabilitiesFor("openai-compatible")
	if !transport.Streaming || !transport.ToolCalling || !transport.Reasoning {
		t.Errorf("transport capabilities = %+v, want streaming/tools/reasoning from the shared adapter", transport)
	}
}
