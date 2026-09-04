package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// isolateModelHome points the Forcefield home at a throwaway dir so tests
// never touch a real ~/.forcefield.
func isolateModelHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	return dir
}

func modelConfigPath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".forcefield", "config.yaml")
}

func writeModelConfig(t *testing.T, body string) {
	t.Helper()
	path := modelConfigPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readModelConfig(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(modelConfigPath(t))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(data)
}

// 1. Empty config (first launch, no file) -> default model is selected.
func TestFirstLaunchUsesDefaultModel(t *testing.T) {
	isolateModelHome(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if strings.TrimSpace(cfg.Model.Name) == "" {
		t.Fatal("default config has empty model.name, want the existing default")
	}
	if strings.TrimSpace(cfg.Model.Provider) == "" {
		t.Fatal("default config has empty model.provider, want the existing default")
	}

	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	if m.modelName != cfg.Model.Name {
		t.Errorf("TUI label = %q, want default %q", m.modelName, cfg.Model.Name)
	}
	if got := m.runtime.CurrentModel(); got != cfg.Model.Name {
		t.Errorf("runtime model = %q, want default %q", got, cfg.Model.Name)
	}
}

// 2. Config containing a saved model -> saved model is selected (not the default).
func TestSavedModelIsSelectedOnLaunch(t *testing.T) {
	isolateModelHome(t)
	writeModelConfig(t, "model:\n  provider: ollama\n  name: saved-model-alpha\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if cfg.Model.Name != "saved-model-alpha" {
		t.Fatalf("loaded model = %q, want saved-model-alpha", cfg.Model.Name)
	}

	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	if m.modelName != "saved-model-alpha" {
		t.Errorf("TUI label = %q, want saved-model-alpha", m.modelName)
	}
	if got := m.runtime.CurrentModel(); got != "saved-model-alpha" {
		t.Errorf("runtime model = %q, want saved-model-alpha", got)
	}
}

// 3. Changing the selected model -> config contains the new model, and the
// runtime (not just the label) uses it.
func TestChangingModelPersistsToConfig(t *testing.T) {
	isolateModelHome(t)
	writeModelConfig(t, "model:\n  provider: ollama\n  name: initial-model-a\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}

	if err := m.SetModel("updated-model-b"); err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	if m.modelName != "updated-model-b" {
		t.Errorf("TUI label = %q, want updated-model-b", m.modelName)
	}
	if got := m.runtime.CurrentModel(); got != "updated-model-b" {
		t.Errorf("runtime model = %q, want updated-model-b", got)
	}
	if data := readModelConfig(t); !strings.Contains(data, "updated-model-b") {
		t.Errorf("config file does not contain updated-model-b; data=%s", data)
	}
}

// 4. Restart/loading config -> previously selected model remains selected.
func TestRestartPreservesSelectedModel(t *testing.T) {
	isolateModelHome(t)
	writeModelConfig(t, "model:\n  provider: ollama\n  name: restart-initial\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	if err := m.SetModel("restart-persisted"); err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}

	// Simulate closing and reopening ff: load from disk and rebuild.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config.Load() error = %v", err)
	}
	if reloaded.Model.Name != "restart-persisted" {
		t.Fatalf("reloaded model = %q, want restart-persisted", reloaded.Model.Name)
	}
	rt, err := runtime.NewFromConfig(reloaded)
	if err != nil {
		t.Fatalf("runtime.NewFromConfig() error = %v", err)
	}
	if got := rt.CurrentModel(); got != "restart-persisted" {
		t.Errorf("restarted runtime model = %q, want restart-persisted", got)
	}

	m2, err := newModel(reloaded, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() after restart error = %v", err)
	}
	if m2.modelName != "restart-persisted" {
		t.Errorf("restarted TUI label = %q, want restart-persisted", m2.modelName)
	}
}

// 5. Provider initialization respects the persisted model: the model sent on
// the wire after a restart is the persisted one, not the old default.
func TestProviderInitUsesPersistedModel(t *testing.T) {
	isolateModelHome(t)

	var gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, _ := body["model"].(string); v != "" {
			gotModel = v
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	writeModelConfig(t, fmt.Sprintf(`model:
  provider: lab
  name: wire-model-one

providers:
  lab:
    type: openai-compatible
    base_url: %s
`, server.URL))

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}

	// Sanity: initial streaming uses the saved model.
	gotModel = ""
	events, err := m.runtime.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	for range events {
	}
	if gotModel != "wire-model-one" {
		t.Fatalf("wire model = %q, want wire-model-one", gotModel)
	}

	// Switch through the same path the picker uses and verify file + runtime.
	if err := m.SetModel("wire-model-two"); err != nil {
		t.Fatalf("SetModel() error = %v", err)
	}
	if data := readModelConfig(t); !strings.Contains(data, "wire-model-two") {
		t.Fatalf("config file missing wire-model-two; data=%s", data)
	}

	// Restart: fresh config + runtime must stream with the new model.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config.Load() error = %v", err)
	}
	rt, err := runtime.NewFromConfig(reloaded)
	if err != nil {
		t.Fatalf("runtime.NewFromConfig() error = %v", err)
	}
	if got := rt.CurrentModel(); got != "wire-model-two" {
		t.Fatalf("restarted runtime model = %q, want wire-model-two", got)
	}
	gotModel = ""
	events, err = rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat() after restart error = %v", err)
	}
	for range events {
	}
	if gotModel != "wire-model-two" {
		t.Errorf("wire model after restart = %q, want wire-model-two (provider init must use persisted model)", gotModel)
	}
}

// chooseModel (the picker handler) must persist as well, since that is the
// path the /model picker takes.
func TestChooseModelPersistsToConfig(t *testing.T) {
	isolateModelHome(t)
	writeModelConfig(t, "model:\n  provider: ollama\n  name: picker-initial\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}
	updated, _ := m.chooseModel("picker-selected")
	m = updated.(model)
	if m.modelName != "picker-selected" {
		t.Errorf("TUI label = %q, want picker-selected", m.modelName)
	}
	if got := m.runtime.CurrentModel(); got != "picker-selected" {
		t.Errorf("runtime model = %q, want picker-selected", got)
	}
	if data := readModelConfig(t); !strings.Contains(data, "picker-selected") {
		t.Errorf("config file does not contain picker-selected; data=%s", data)
	}
}
