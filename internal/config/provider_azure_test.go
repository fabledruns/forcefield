package config

import (
	"strings"
	"testing"
)

func TestAzureResolvesResource(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: az\n  name: gpt4o\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    api_version: 2025-01-01-preview\n    models:\n      - gpt4o\n      - o4-mini\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("az", "gpt4o")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.Type != "azure" {
		t.Errorf("type = %q, want azure", resolved.Type)
	}
	if resolved.BaseURL != "https://myres.openai.azure.com" {
		t.Errorf("baseURL = %q, want the resource endpoint", resolved.BaseURL)
	}
	if resolved.APIVersion != "2025-01-01-preview" {
		t.Errorf("apiVersion = %q, want the configured version", resolved.APIVersion)
	}
	if resolved.AuthEnvVar != "AZURE_OPENAI_API_KEY" {
		t.Errorf("authEnvVar = %q, want the catalog default", resolved.AuthEnvVar)
	}
	if !resolved.AuthRequired {
		t.Error("azure must require credentials (api key or Entra token)")
	}
	spec := resolved.Spec("gpt4o")
	if err := spec.Validate(); err != nil {
		t.Errorf("Spec.Validate() error = %v", err)
	}
	if spec.APIVersion != "2025-01-01-preview" {
		t.Errorf("spec.APIVersion = %q, want the configured version", spec.APIVersion)
	}
	if len(resolved.Models) != 2 {
		t.Errorf("models = %v, want the configured deployments", resolved.Models)
	}
}

func TestAzureEntraBearerToken(t *testing.T) {
	isolateHome(t)
	t.Setenv("AZURE_ENTRA_TOKEN", "entra-token-value")
	writeProvidersConfig(t, "model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    auth_mode: bearer\n    api_key_env: AZURE_ENTRA_TOKEN\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("az", "m")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.APIKey != "entra-token-value" {
		t.Errorf("apiKey = %q, want the Entra token from the env", resolved.APIKey)
	}
	if resolved.AuthMode != "bearer" {
		t.Errorf("authMode = %q, want bearer", resolved.AuthMode)
	}
	if err := resolved.Spec("m").Validate(); err != nil {
		t.Errorf("Spec.Validate() error = %v", err)
	}
}

func TestAzureValidationRejects(t *testing.T) {
	cases := map[string]struct {
		body     string
		wantText string
	}{
		"auth_header rejected": {
			"model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    auth_mode: header\n    auth_header: X-Api\n",
			"auth_header",
		},
		"auth_scheme rejected": {
			"model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    auth_scheme: Token\n",
			"auth_scheme",
		},
		"unsupported auth_mode rejected": {
			"model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    auth_mode: header\n",
			"auth_mode",
		},
		"api_path rejected": {
			"model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    api_path: /custom\n",
			"api_path",
		},
		"api_version on openai rejected": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    api_version: 2024-06-01\n",
			"only supported for",
		},
		"api_version charset rejected": {
			"model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    api_version: \"2024-06-01&x=1\"\n",
			"api_version",
		},
		"missing base_url rejected": {
			"model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n",
			"base_url",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeProvidersConfig(t, tc.body)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() accepted invalid azure config")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error = %q, want %q", err, tc.wantText)
			}
		})
	}
}
