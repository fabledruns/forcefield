package config

import (
	"strings"
	"testing"
)

func TestVertexResolvesRegionalEndpoint(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: vertex\n  name: gemini-2.5-flash\nproviders:\n  vertex:\n    type: vertex\n    project: my-proj\n    location: us-central1\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("vertex", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.Type != "vertex" {
		t.Errorf("type = %q, want vertex", resolved.Type)
	}
	if resolved.BaseURL != "https://us-central1-aiplatform.googleapis.com" {
		t.Errorf("baseURL = %q, want derived regional endpoint", resolved.BaseURL)
	}
	if resolved.Project != "my-proj" || resolved.Location != "us-central1" {
		t.Errorf("project/location = %q/%q", resolved.Project, resolved.Location)
	}
	if resolved.AuthRequired {
		t.Error("vertex must not require an API key (ADC owns auth)")
	}
	if err := resolved.Spec(cfg.Model.Name).Validate(); err != nil {
		t.Errorf("Spec.Validate() error = %v", err)
	}
}

func TestVertexExplicitBaseURLOverride(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: v\n  name: m\nproviders:\n  v:\n    type: vertex\n    base_url: https://custom.example.com\n    project: p\n    location: us-central1\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("v", "m")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.BaseURL != "https://custom.example.com" {
		t.Errorf("baseURL = %q, want explicit override kept", resolved.BaseURL)
	}
}

func TestVertexValidationRejects(t *testing.T) {
	cases := map[string]struct {
		body     string
		wantText string
	}{
		"missing location": {
			"model:\n  provider: v\n  name: m\nproviders:\n  v:\n    type: vertex\n    project: p\n",
			"location",
		},
		"api_key_env rejected": {
			"model:\n  provider: v\n  name: m\nproviders:\n  v:\n    type: vertex\n    project: p\n    location: us-central1\n    api_key_env: FOO\n",
			"Application Default Credentials",
		},
		"auth_mode rejected": {
			"model:\n  provider: v\n  name: m\nproviders:\n  v:\n    type: vertex\n    project: p\n    location: us-central1\n    auth_mode: bearer\n",
			"Application Default Credentials",
		},
		"api_path rejected": {
			"model:\n  provider: v\n  name: m\nproviders:\n  v:\n    type: vertex\n    project: p\n    location: us-central1\n    api_path: /custom\n",
			"api_path",
		},
		"missing credentials file": {
			"model:\n  provider: v\n  name: m\nproviders:\n  v:\n    type: vertex\n    project: p\n    location: us-central1\n    credentials_file: /nonexistent-key.json\n",
			"credentials_file",
		},
		"vertex fields on openai": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    project: p\n",
			"only supported for",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeProvidersConfig(t, tc.body)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() accepted invalid vertex config")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error = %q, want %q", err, tc.wantText)
			}
		})
	}
}

func TestVertexProjectFromEnv(t *testing.T) {
	isolateHome(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "env-proj")
	writeProvidersConfig(t, "model:\n  provider: v\n  name: m\nproviders:\n  v:\n    type: vertex\n    location: europe-west1\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("v", "m")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.Project != "env-proj" {
		t.Errorf("project = %q, want env fallback", resolved.Project)
	}
	if resolved.BaseURL != "https://europe-west1-aiplatform.googleapis.com" {
		t.Errorf("baseURL = %q, want european endpoint", resolved.BaseURL)
	}
}

func TestVertexMissingProject(t *testing.T) {
	isolateHome(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	writeProvidersConfig(t, "model:\n  provider: v\n  name: m\nproviders:\n  v:\n    type: vertex\n    location: us-central1\n")

	// The active provider cannot resolve without a project anywhere, so
	// Load fails fast naming the missing field.
	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a vertex provider with no project")
	}
	if !strings.Contains(err.Error(), "project") {
		t.Errorf("error = %q, want it to name project", err)
	}
}
