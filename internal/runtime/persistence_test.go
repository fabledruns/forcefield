package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetModelIsInMemoryOnly(t *testing.T) {
	isolateRuntimeHome(t)
	writeConfigFile(t, "model:\n  provider: ollama\n  name: ornith:9b\n")
	rt, err := New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if err := rt.SetModel("new-model"); err != nil {
		t.Fatalf("SetModel = %v", err)
	}
	// In-memory should reflect new model
	if rt.CurrentModel() != "new-model" {
		t.Fatalf("CurrentModel = %q, want new-model", rt.CurrentModel())
	}
	// File should still contain old model
	path := filepath.Join(homeDir(t), ".forcefield", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config = %v", err)
	}
	if stringContains(string(data), "new-model") {
		t.Fatalf("config file was rewritten with new-model, want temporary switch only; data=%s", string(data))
	}
	// Explicit Save should persist
	if err := rt.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig = %v", err)
	}
	data, _ = os.ReadFile(path)
	if !stringContains(string(data), "new-model") {
		t.Fatalf("SaveConfig did not persist new-model; data=%s", string(data))
	}
}

func TestSetProviderIsInMemoryOnly(t *testing.T) {
	isolateRuntimeHome(t)
	serverURL := "http://localhost:59999/v1"
	writeConfigFile(t, "model:\n  provider: ollama\n  name: ornith:9b\nproviders:\n  lab:\n    type: openai-compatible\n    base_url: "+serverURL+"\n")
	rt, err := New()
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if err := rt.SetProvider("lab"); err != nil {
		t.Fatalf("SetProvider = %v", err)
	}
	if rt.CurrentProvider() != "lab" {
		t.Fatalf("CurrentProvider = %q, want lab", rt.CurrentProvider())
	}
	path := filepath.Join(homeDir(t), ".forcefield", "config.yaml")
	data, _ := os.ReadFile(path)
	if stringContains(string(data), "provider: lab") {
		t.Fatalf("config file prematurely persisted lab provider")
	}
	if err := rt.SaveConfig(); err != nil {
		t.Fatalf("SaveConfig = %v", err)
	}
	data, _ = os.ReadFile(path)
	if !stringContains(string(data), "lab") {
		t.Fatalf("SaveConfig should have persisted lab")
	}
}

func stringContains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
