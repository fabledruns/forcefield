package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/providers"
)

func TestRuntimeLemonadeContextBudgetAndEnableThinking(t *testing.T) {
	var sawEnableThinking *bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models/Qwen3-4B/options":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"resolved_ctx_size": 12288}`)
		case "/api/v1/chat/completions":
			var req struct {
				EnableThinking *bool `json:"enable_thinking"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			sawEnableThinking = req.EnableThinking

			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfgDir := filepath.Join(home, ".forcefield")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgYAML := fmt.Sprintf(`model:
  provider: custom-lemon
  name: Qwen3-4B
providers:
  custom-lemon:
    type: lemonade
    base_url: %s/api/v1
`, srv.URL)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	rt, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	// Verify reasoning toggle works even under custom alias with type: lemonade.
	caps := rt.CurrentReasoningCapabilities()
	if !caps.SupportsThinking() || caps.Thinking.Kind != providers.ThinkingKindBool {
		t.Fatalf("CurrentReasoningCapabilities() = %+v, want ThinkingKindBool", caps)
	}
	enabled, err := rt.ToggleThinking()
	if err != nil || !enabled {
		t.Fatalf("ToggleThinking() = (%v, %v), want (true, nil)", enabled, err)
	}

	evCh, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hello"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	for ev := range evCh {
		if ev.Type == EventError {
			t.Fatalf("unexpected runtime error event: %v", ev.Err)
		}
	}

	if sawEnableThinking == nil || !*sawEnableThinking {
		t.Fatalf("expected enable_thinking=true on Lemonade wire payload, got %v", sawEnableThinking)
	}

	// Verify provider cache now holds resolved_ctx_size=12288 and feeds Capabilities/BudgetForCaps.
	rt.mu.RLock()
	prov := rt.provider
	rt.mu.RUnlock()
	if got := providers.ResolveCapabilities(prov, "Qwen3-4B").ContextWindow; got != 12288 {
		t.Errorf("resolved ContextWindow = %d, want 12288", got)
	}
}
