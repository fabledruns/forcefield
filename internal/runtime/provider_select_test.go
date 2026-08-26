package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/providers"
)

const lmstudioSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"local reply\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n" +
	"\n" +
	"data: [DONE]\n\n"

// isolateRuntimeHome points the Forcefield home directory at a throwaway
// dir so tests never read or write a real ~/.forcefield.
func isolateRuntimeHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir) // os.UserHomeDir on Windows
	t.Setenv("HOME", dir)        // os.UserHomeDir elsewhere
}

func writeConfigFile(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(homeDir(t), ".forcefield", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	return cfg
}

func writeMultiProviderConfig(t *testing.T, localURL string) {
	t.Helper()
	writeConfigFile(t, fmt.Sprintf(`model:
  provider: ollama
  name: ornith:9b

providers:
  ollama:
    type: ollama

  lab:
    type: openai-compatible
    base_url: %s
`, localURL))
}

func homeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProviderForBuildsAdapterPerProtocol(t *testing.T) {
	isolateRuntimeHome(t)
	writeMultiProviderConfig(t, "http://localhost:59999/v1")

	cfg := loadTestConfig(t)

	ollamaP, err := ProviderFor(cfg, "ollama")
	if err != nil {
		t.Fatalf("ProviderFor(ollama) error = %v", err)
	}
	if _, ok := ollamaP.(*providers.OllamaProvider); !ok {
		t.Errorf("ollama built %T, want *providers.OllamaProvider", ollamaP)
	}

	customP, err := ProviderFor(cfg, "lab")
	if err != nil {
		t.Fatalf("ProviderFor(lab) error = %v", err)
	}
	if _, ok := customP.(*providers.OpenAICompatible); !ok {
		t.Errorf("custom entry built %T, want *providers.OpenAICompatible", customP)
	}

	if _, err := ProviderFor(cfg, "ghost"); err == nil {
		t.Error("ProviderFor(ghost) succeeded for an unconfigured provider")
	}
}

func TestNewUsesConfiguredProviderWithoutBranching(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, lmstudioSSE)
	}))
	defer server.Close()

	isolateRuntimeHome(t)
	writeConfigFile(t, fmt.Sprintf(
		"model:\n  provider: lab\n  name: test-model\n\nproviders:\n  lab:\n    type: openai-compatible\n    base_url: %s\n",
		server.URL,
	))

	rt, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if rt.CurrentProvider() != "lab" {
		t.Errorf("CurrentProvider() = %q, want lab", rt.CurrentProvider())
	}

	resp, err := rt.RunContext(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hello"}})
	if err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	if resp.Content != "local reply" {
		t.Errorf("response = %q, want the mock server's reply", resp.Content)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("request path = %q, want /chat/completions", gotPath)
	}
	if resp.Usage.TotalTokens != 6 {
		t.Errorf("usage = %#v, want total 6 parsed from stream", resp.Usage)
	}
}

func TestSetProviderSwitchesWithoutRestartAndStreamsFromNewServer(t *testing.T) {
	var openaiRequests int
	openaiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiRequests++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"from custom server"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer openaiServer.Close()

	isolateRuntimeHome(t)
	writeConfigFile(t, fmt.Sprintf(
		"model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: ornith:9b\n\nproviders:\n  lab:\n    type: openai-compatible\n    base_url: %s\n",
		openaiServer.URL,
	))

	rt, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Switch provider + model without restarting; next request uses them.
	if err := rt.SetModel("test-model"); err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	if err := rt.SetProvider("lab"); err != nil {
		t.Fatalf("SetProvider(lab) error = %v", err)
	}
	if rt.CurrentProvider() != "lab" || rt.CurrentModel() != "test-model" {
		t.Fatalf("active = %s/%s, want lab/test-model after switching", rt.CurrentProvider(), rt.CurrentModel())
	}

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	var final *providers.Response
	for event := range events {
		switch event.Type {
		case EventError:
			t.Fatalf("run errored after switch: %v", event.Err)
		case EventDone:
			final = event.Response
		}
	}
	if final == nil || final.Content != "from custom server" {
		t.Fatalf("final response = %#v, want the new provider's reply", final)
	}
	if openaiRequests != 1 {
		t.Errorf("requests to new provider = %d, want 1", openaiRequests)
	}
}

func TestStreamChatReportsMissingAPIKeyWithGuidance(t *testing.T) {
	isolateRuntimeHome(t)
	writeConfigFile(t, "model:\n  provider: anthropic\n  name: claude-x\n")
	t.Setenv("ANTHROPIC_API_KEY", "")

	rt, err := New()
	if err != nil {
		t.Fatalf("New() error = %v (a missing key must not stop startup)", err)
	}

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	sawErr := ""
	for event := range events {
		if event.Type == EventError && sawErr == "" {
			sawErr = event.Err.Error()
		}
	}
	if sawErr == "" {
		t.Fatal("run completed without an auth error despite missing key")
	}
	if !strings.Contains(sawErr, "ANTHROPIC_API_KEY") {
		t.Errorf("error = %q, want guidance naming ANTHROPIC_API_KEY", sawErr)
	}
}

func TestProviderSummariesExposeCapabilitiesAndAvailability(t *testing.T) {
	isolateRuntimeHome(t)
	writeConfigFile(t,
		"model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: ornith:9b\n")

	rt, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	summaries := rt.ProviderSummaries()
	byID := map[string]ProviderSummary{}
	for _, s := range summaries {
		byID[s.ID] = s
	}

	ollama, ok := byID["ollama"]
	if !ok {
		t.Fatal("ollama missing from summaries")
	}
	if !strings.Contains(ollama.Detail, "local") || !strings.Contains(ollama.Detail, "tools") {
		t.Errorf("ollama detail = %q, want local + tools", ollama.Detail)
	}

	openai, ok := byID["openai"]
	if !ok {
		t.Fatal("openai missing from summaries")
	}
	if !strings.Contains(openai.Detail, "cloud") || !strings.Contains(openai.Detail, "api key missing") {
		t.Errorf("openai detail = %q, want cloud + api key missing", openai.Detail)
	}
	if openai.Available {
		t.Error("openai reported available with no key configured")
	}

	lmstudio := byID["lmstudio"]
	if len(lmstudio.Models) == 0 {
		t.Error("lmstudio models empty; catalog defaults expected")
	}
}
