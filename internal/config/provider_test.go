package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProvidersConfig(t *testing.T, body string) {
	t.Helper()
	writeConfig(t, body)
}

func TestResolveProviderUsesCatalogDefaults(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: openai\n  name: gpt-4o-mini\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("openai", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider(openai) error = %v", err)
	}
	if resolved.Type != "openai-compatible" {
		t.Errorf("type = %q, want the openai-compatible protocol", resolved.Type)
	}
	if resolved.Label != "OpenAI" || resolved.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("label/base = %q/%q, want OpenAI catalog defaults", resolved.Label, resolved.BaseURL)
	}
	if !resolved.AuthRequired || resolved.AuthEnvVar != "OPENAI_API_KEY" {
		t.Errorf("auth = %v/%q, want required via OPENAI_API_KEY", resolved.AuthRequired, resolved.AuthEnvVar)
	}
	if len(resolved.Models) != 2 {
		t.Errorf("models = %v, want the two catalog defaults", resolved.Models)
	}
}

func TestResolveProviderCustomEntryOverrides(t *testing.T) {
	isolateHome(t)
	body := `model:
  provider: local
  name: qwen

providers:
  local:
    type: openai-compatible
    base_url: http://localhost:1234/v1/
    headers:
      X-Custom: "1"
`
	writeProvidersConfig(t, body)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("local", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider(local) error = %v", err)
	}
	if resolved.Type != "openai-compatible" || resolved.BaseURL != "http://localhost:1234/v1" {
		t.Errorf("resolved = %+v, want trailing slash trimmed", resolved)
	}
	spec := resolved.Spec(cfg.Model.Name)
	if spec.Model != "qwen" {
		t.Errorf("spec model = %q, want the active model", spec.Model)
	}
	if spec.Headers["X-Custom"] != "1" {
		t.Errorf("headers = %#v, want custom header copied", spec.Headers)
	}
}

func TestResolveProviderTypeAliasAdoptsServiceDefaults(t *testing.T) {
	isolateHome(t)
	body := `
model:
  provider: work-nim
  name: m
  endpoint: https://integrate.api.nvidia.com/v1

providers:
  work-nim:
    type: nvidia
`
	writeProvidersConfig(t, body)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("work-nim", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider(work-nim) error = %v", err)
	}
	if resolved.Label != "NVIDIA NIM" || resolved.AuthEnvVar != "NVIDIA_API_KEY" || !resolved.AuthRequired {
		t.Errorf("alias resolution = %+v, want NVIDIA service defaults adopted", resolved)
	}
}

func TestResolveProviderLegacyFallbackWithoutSection(t *testing.T) {
	isolateHome(t)

	// Legacy single-provider config: no providers section at all.
	writeProvidersConfig(t, "model:\n  provider: lmstudio\n  endpoint: http://localhost:1234/v1\n  name: local-model\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v (legacy config must keep working)", err)
	}
	resolved, err := cfg.ResolveProvider("lmstudio", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider(lmstudio) error = %v", err)
	}
	if resolved.Type != "openai-compatible" || resolved.BaseURL != "http://localhost:1234/v1" {
		t.Errorf("legacy lmstudio resolution = %+v", resolved)
	}
	if resolved.AuthRequired {
		t.Error("LM Studio must not require an API key")
	}
}

func TestResolveProviderUnknownIDErrors(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: ollama\n  endpoint: http://x\n  name: m\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, err = cfg.ResolveProvider("ghost", "")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error = %v, want not-configured message", err)
	}
}

func TestAPIKeyResolutionPerProvider(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("OPENAI_API_KEY=from-openai-env\n"), 0o644)

	body := `model:
  provider: openai
  name: gpt-4o-mini
`
	writeProvidersConfig(t, body)
	t.Setenv("OPENAI_API_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("openai", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.APIKey != "from-openai-env" {
		t.Errorf("key = %q, want the .env value", resolved.APIKey)
	}
	if resolved.APIKeySource == "" || !strings.Contains(resolved.APIKeySource, ".env") {
		t.Errorf("source = %q, want it to name the .env file", resolved.APIKeySource)
	}

	// Environment wins over .env.
	t.Setenv("OPENAI_API_KEY", "from-process-env")
	envResolved, _ := cfg.ResolveProvider("openai", cfg.Model.Name)
	if envResolved.APIKey != "from-process-env" {
		t.Errorf("key = %q, want environment precedence", envResolved.APIKey)
	}
}

func TestCustomAPIKeyEnvVariable(t *testing.T) {
	isolateHome(t)
	body := `model:
  provider: orouter
  name: m

providers:
  orouter:
    type: openrouter
    api_key_env: MY_CUSTOM_KEY
`
	writeProvidersConfig(t, body)
	t.Setenv("MY_CUSTOM_KEY", "custom-value")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("orouter", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.APIKey != "custom-value" || resolved.AuthEnvVar != "MY_CUSTOM_KEY" {
		t.Errorf("resolution = key %q env %q, want custom variable honored", resolved.APIKey, resolved.AuthEnvVar)
	}
}

func TestValidationRejectsBadProviderEntries(t *testing.T) {
	cases := map[string]struct {
		body     string
		wantText string
	}{
		"unknown type": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: telegram\n",
			"type",
		},
		"bad scheme": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: ftp://nope\n",
			"http",
		},
		"missing host": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: 'http://'\n",
			"host",
		},
		"bad header name": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    headers:\n      \"Bad Header\": v\n",
			"header",
		},
		"empty model entry": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: ollama\n    models:\n      - \"\"\n",
			"models",
		},
		"bad api_key_env name": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai\n    api_key_env: \"9 bad\"\n",
			"api_key_env",
		},
		"unknown active provider": {
			"model:\n  provider: ghost\n  name: m\n",
			"not configured",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeProvidersConfig(t, tc.body)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load() accepted invalid config")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantText)
			}
		})
	}
}

func TestSaveNeverPersistsSecretsFromProvidersSection(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("OPENAI_API_KEY=sk-live-secret\n"), 0o644)
	t.Setenv("OPENAI_API_KEY", "")

	body := "model:\n  provider: openai\n  name: gpt-4o-mini\n"
	writeProvidersConfig(t, body)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	raw, err := os.ReadFile(mustPath(t))
	if err != nil {
		t.Fatal(err)
	}
	saved := string(raw)
	if strings.Contains(saved, "sk-live-secret") {
		t.Fatal("the resolved API key value was written to config.yaml")
	}
	if strings.Contains(saved, "api_key") && !strings.Contains(saved, "api_key_env") {
		t.Errorf("saved config contains an unexpected api_key field:\n%s", saved)
	}
}

func TestResolveAllIncludesCatalogAndCustomInOrder(t *testing.T) {
	isolateHome(t)
	body := `model:
  provider: zz-local
  endpoint: http://localhost:9/v1
  name: m

providers:
  zz-local:
    type: openai-compatible
    base_url: http://localhost:9/v1
`
	writeProvidersConfig(t, body)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, failures := cfg.ResolveAll("")
	if len(failures) != 0 {
		t.Fatalf("failures = %v, want none for a valid config", failures)
	}
	if len(resolved) == 0 {
		t.Fatal("ResolveAll returned nothing")
	}
	// Catalog order first.
	if resolved[0].ID != "ollama" {
		t.Errorf("first = %q, want ollama (catalog order)", resolved[0].ID)
	}
	// Custom entries come last, sorted.
	last := resolved[len(resolved)-1]
	if last.ID != "zz-local" {
		t.Errorf("last = %q, want the custom entry zz-local", last.ID)
	}
}

func TestModelEndpointOptionalForCatalogProviders(t *testing.T) {
	isolateHome(t)
	// No endpoint at all: anthropic's catalog default applies.
	writeProvidersConfig(t, "model:\n  provider: anthropic\n  name: claude-x\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("anthropic", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider(anthropic) error = %v", err)
	}
	if resolved.BaseURL != "https://api.anthropic.com" {
		t.Errorf("base URL = %q, want the Anthropic default", resolved.BaseURL)
	}
}

func TestLegacyNVIDIAKeyStillResolvesIntoModelField(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("NVIDIA_API_KEY=nvapi-legacy\n"), 0o644)
	t.Setenv(apiKeyName, "")

	body := "model:\n  provider: nvidia\n  name: test-model\n"
	writeProvidersConfig(t, body)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Model.APIKey != "nvapi-legacy" {
		t.Errorf("cfg.Model.APIKey = %q, want the legacy convenience field populated", cfg.Model.APIKey)
	}
}
