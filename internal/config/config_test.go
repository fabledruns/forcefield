package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome points the Forcefield home directory at a fresh temp dir
// for the duration of the test, so tests never touch a real ~/.forcefield.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("HOME", home)        // os.UserHomeDir everywhere else
	return home
}

func TestLoadCreatesValidDefaultOnFirstRun(t *testing.T) {
	isolateHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Model.Provider == "" || cfg.Model.Endpoint == "" || cfg.Model.Name == "" {
		t.Fatalf("default config missing model fields: %+v", cfg.Model)
	}
	if cfg.Permissions.Default != "ask" && cfg.Permissions.Default != "" {
		t.Errorf("default permissions = %q, want ask or empty", cfg.Permissions.Default)
	}

	// The generated file must itself be loadable: a default that fails its
	// own validation would brick first-run entirely.
	if _, err := os.Stat(mustPath(t)); err != nil {
		t.Fatalf("default config file not created: %v", err)
	}
	if _, err := Load(); err != nil {
		t.Fatalf("re-Load() of created default error = %v", err)
	}
}

func mustPath(t *testing.T) string {
	t.Helper()
	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	return p
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := mustPath(t)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	isolateHome(t)
	path := writeConfig(t, "model: [unclosed")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() on malformed YAML returned no error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not mention the config path", err)
	}
}

func TestLoadRejectsMissingRequiredFields(t *testing.T) {
	for name, body := range map[string]string{
		"provider": "model:\n  endpoint: http://localhost:11434\n  name: m\n",
		"name":     "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n",
	} {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeConfig(t, body)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() accepted config missing model.%s", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name the missing field %q", err, name)
			}
		})
	}
}

// TestLoadDefaultsEndpointFromCatalog pins that known providers get their
// default base URL from the built-in catalog, so a cloud provider can be
// selected without repeating its endpoint.
func TestLoadDefaultsEndpointFromCatalog(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: openai\n  name: gpt-4o-mini\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("openai", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider(openai) error = %v", err)
	}
	if resolved.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("base URL = %q, want the OpenAI catalog default", resolved.BaseURL)
	}
}

// TestLoadRejectsCustomProviderWithoutAnyEndpoint makes sure a provider
// with no catalog default still fails loudly when no endpoint is given.
func TestLoadRejectsCustomProviderWithoutAnyEndpoint(t *testing.T) {
	isolateHome(t)
	writeConfig(t,
		"model:\n  provider: local-llm\n  name: m\n"+
			"providers:\n  local-llm:\n    type: openai-compatible\n")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a custom provider with no base_url")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Errorf("error %q does not point at the missing base_url", err)
	}
}

func TestLoadRejectsInvalidPermissionValues(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://x\n  name: m\npermissions:\n  default: sometimes\n")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted invalid permissions.default")
	}
	if !strings.Contains(err.Error(), "permissions.default") {
		t.Errorf("error %q does not name permissions.default", err)
	}
	if !strings.Contains(err.Error(), "sometimes") {
		t.Errorf("error %q does not quote the invalid value", err)
	}
}

func TestSaveRoundTripsAndNeverWritesAPIKey(t *testing.T) {
	isolateHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg.Permissions.Tools = map[string]string{"shell": "allow"}
	cfg.Model.APIKey = "super-secret-key"

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	raw, err := os.ReadFile(mustPath(t))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(raw), "super-secret-key") {
		t.Fatal("API key was written to disk")
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatalf("re-Load() error = %v", err)
	}
	if reloaded.Permissions.Tools["shell"] != "allow" {
		t.Errorf("tools.shell = %q after round-trip, want allow", reloaded.Permissions.Tools["shell"])
	}
}

func TestSaveIsAtomicAndLeavesNoDebris(t *testing.T) {
	isolateHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for i := 0; i < 3; i++ {
		cfg.Model.Name = "model-" + strings.Repeat("x", i+1)
		if err := cfg.Save(); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	dir := filepath.Dir(mustPath(t))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read home dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	final, err := Load()
	if err != nil {
		t.Fatalf("final Load() error = %v", err)
	}
	if final.Model.Name != cfg.Model.Name {
		t.Errorf("final model = %q, want %q", final.Model.Name, cfg.Model.Name)
	}
}

func TestValidatePermissionValues(t *testing.T) {
	for _, valid := range []string{"", "allow", "deny", "ask"} {
		if err := validatePermissionValue("f", valid); err != nil {
			t.Errorf("validatePermissionValue(%q) = %v, want nil", valid, err)
		}
	}
	if err := validatePermissionValue("f", "maybe"); err == nil {
		t.Error("validatePermissionValue(\"maybe\") = nil, want error")
	}
}
