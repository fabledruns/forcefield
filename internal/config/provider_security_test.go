package config

import (
	"strings"
	"testing"
)

func TestResolveRejectsBadAPIKeyWithoutLeak(t *testing.T) {
	isolateHome(t)
	const secret = "sk-bad\nkey"
	t.Setenv("FF_TEST_BAD_KEY_XYZ", secret)
	writeProvidersConfig(t, "model:\n  provider: custom\n  name: m\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: http://localhost:9/v1\n    api_key_env: FF_TEST_BAD_KEY_XYZ\n")

	// Load resolves the active provider, so a bad credential fails fast
	// at startup without ever reaching the network.
	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a CRLF key")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sk-bad") {
		t.Fatalf("error leaked the key: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "FF_TEST_BAD_KEY_XYZ") {
		t.Errorf("error = %q, want it to name the variable", err)
	}
}

func TestMetadataBlockedByDefault(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: bad\n  name: m\nproviders:\n  bad:\n    type: openai-compatible\n    base_url: http://169.254.169.254/v1\n")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a metadata endpoint without opt-in")
	} else if !strings.Contains(strings.ToLower(err.Error()), "metadata") {
		t.Errorf("error = %q, want metadata mention", err)
	}
}

func TestMetadataOptInAllowed(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: meta\n  name: m\nproviders:\n  meta:\n    type: openai-compatible\n    base_url: http://169.254.169.254/v1\n    allow_metadata_endpoint: true\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with opt-in error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("meta", "m")
	if err != nil {
		t.Fatalf("ResolveProvider with opt-in error = %v", err)
	}
	if !resolved.AllowMetadataEndpoint {
		t.Error("AllowMetadataEndpoint = false, want true")
	}
	if err := resolved.Spec("m").Validate(); err != nil {
		t.Errorf("Spec.Validate() with opt-in error = %v", err)
	}
}

func TestLocalhostPreserved(t *testing.T) {
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: local\n  name: m\nproviders:\n  local:\n    type: openai-compatible\n    base_url: http://localhost:11434/v1\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v (localhost must keep working)", err)
	}
	if _, err := cfg.ResolveProvider("local", "m"); err != nil {
		t.Fatalf("ResolveProvider(localhost) error = %v", err)
	}
}
