package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forcefield/internal/providers"
)

// TestSetProviderToOpenCodeWithStaleModel reproduces the picker flow from
// the bug report: the active model belongs to the previous provider, so
// switching to opencode-zen must still succeed and offer the Zen model
// list instead of erroring on the stale name.
func TestSetProviderToOpenCodeWithStaleModel(t *testing.T) {
	isolateRuntimeHome(t)
	t.Setenv("OPENCODE_API_KEY", "test-key")
	writeConfigFile(t, "model:\n  provider: nvidia\n  name: nvidia/nemotron-3-ultra-550b-a55b\n")

	rt, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Switching providers with a foreign model name must succeed so the
	// model picker can open.
	if err := rt.SetProvider("opencode-zen"); err != nil {
		t.Fatalf("SetProvider(opencode-zen) with stale model error = %v", err)
	}
	if rt.CurrentProvider() != "opencode-zen" {
		t.Errorf("CurrentProvider() = %q, want opencode-zen", rt.CurrentProvider())
	}

	// The catalog for the new provider must offer Zen models.
	models, _ := rt.ModelCatalog("opencode-zen")
	found := false
	for _, m := range models {
		if m.ID == "gpt-5.5" {
			found = true
		}
	}
	if !found {
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		t.Fatalf("zen catalog lacks gpt-5.5, got %v", ids)
	}

	// A turn before picking a model must fail locally with guidance,
	// never send a request down a guessed protocol.
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	sawGuidance := false
	for event := range events {
		if event.Type == EventError && strings.Contains(event.Err.Error(), "no model configured") {
			sawGuidance = true
		}
	}
	if !sawGuidance {
		t.Error("expected a local no-model-configured error before model selection")
	}
}

// TestSetProviderToOpenCodeThenPickModel completes the flow: after the
// switch, picking a Zen model streams through the Responses transport.
func TestSetProviderToOpenCodeThenPickModel(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"zen reply\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer srv.Close()

	isolateRuntimeHome(t)
	t.Setenv("OPENCODE_API_KEY", "test-key")
	writeConfigFile(t, "model:\n  provider: nvidia\n  name: nvidia/nemotron-3-ultra-550b-a55b\nproviders:\n  myzen:\n    type: opencode-zen\n    base_url: "+srv.URL+"\n")

	rt, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rt.SetProvider("myzen"); err != nil {
		t.Fatalf("SetProvider(myzen) error = %v", err)
	}
	if err := rt.SetModel("gpt-5.5"); err != nil {
		t.Fatalf("SetModel(gpt-5.5) error = %v", err)
	}

	resp, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	if resp.Content != "zen reply" {
		t.Errorf("response = %q, want zen reply", resp.Content)
	}
	if gotPath != "/responses" {
		t.Errorf("request path = %q, want /responses", gotPath)
	}
}

// TestNewWithStaleOpenCodeModelStarts ensures a config already pointing at
// an OpenCode provider with a foreign model name still boots.
func TestNewWithStaleOpenCodeModelStarts(t *testing.T) {
	isolateRuntimeHome(t)
	writeConfigFile(t, "model:\n  provider: opencode-go\n  name: moonshotai/kimi-k3\n")

	rt, err := New()
	if err != nil {
		t.Fatalf("New() with stale opencode model error = %v", err)
	}
	if rt.CurrentProvider() != "opencode-go" {
		t.Errorf("CurrentProvider() = %q", rt.CurrentProvider())
	}
}
