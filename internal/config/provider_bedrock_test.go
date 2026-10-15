package config

import (
	"strings"
	"testing"
)

func TestBedrockResolvesRegion(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: br\n  name: m\nproviders:\n  br:\n    type: bedrock\n    region: us-east-1\n    models:\n      - test-model\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("br", "m")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.Type != "bedrock" || resolved.Region != "us-east-1" {
		t.Errorf("type/region = %q/%q, want bedrock/us-east-1", resolved.Type, resolved.Region)
	}
	if resolved.AuthRequired {
		t.Error("bedrock must not require an API key (chain owns auth)")
	}
	if resolved.BaseURL != "" {
		t.Errorf("baseURL = %q, want empty (SDK resolves endpoint)", resolved.BaseURL)
	}
	if err := resolved.Spec("m").Validate(); err != nil {
		t.Errorf("Spec.Validate() error = %v", err)
	}
	if len(resolved.Models) != 1 || resolved.Models[0] != "test-model" {
		t.Errorf("models = %v, want configured fallback", resolved.Models)
	}
}

func TestBedrockRegionFromEnv(t *testing.T) {
	isolateHome(t)
	t.Setenv("AWS_REGION", "eu-west-1")
	writeProvidersConfig(t, "model:\n  provider: br\n  name: m\nproviders:\n  br:\n    type: bedrock\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("br", "m")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.Region != "eu-west-1" {
		t.Errorf("region = %q, want env fallback", resolved.Region)
	}
}

func TestBedrockValidationRejects(t *testing.T) {
	cases := map[string]struct {
		body     string
		wantText string
	}{
		"api_key_env rejected": {
			"model:\n  provider: br\n  name: m\nproviders:\n  br:\n    type: bedrock\n    region: us-east-1\n    api_key_env: FOO\n",
			"credential chain",
		},
		"auth_mode rejected": {
			"model:\n  provider: br\n  name: m\nproviders:\n  br:\n    type: bedrock\n    region: us-east-1\n    auth_mode: bearer\n",
			"SigV4",
		},
		"api_path rejected": {
			"model:\n  provider: br\n  name: m\nproviders:\n  br:\n    type: bedrock\n    region: us-east-1\n    api_path: /custom\n",
			"api_path",
		},
		"bedrock fields on openai": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    region: us-east-1\n",
			"only supported for",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeProvidersConfig(t, tc.body)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() accepted invalid bedrock config")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error = %q, want %q", err, tc.wantText)
			}
		})
	}
}

func TestBedrockMissingRegion(t *testing.T) {
	isolateHome(t)
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	writeProvidersConfig(t, "model:\n  provider: br\n  name: m\nproviders:\n  br:\n    type: bedrock\n")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted bedrock with no region")
	}
	if !strings.Contains(err.Error(), "region") {
		t.Errorf("error = %q, want it to name region", err)
	}
}

func TestBedrockProfilePassthrough(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: br\n  name: m\nproviders:\n  br:\n    type: bedrock\n    region: us-east-1\n    profile: work\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("br", "m")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.Profile != "work" {
		t.Errorf("profile = %q, want work", resolved.Profile)
	}
	if resolved.Spec("m").Profile != "work" {
		t.Error("spec lost the profile")
	}
}
