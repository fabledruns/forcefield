package cmd

import (
	"net/http"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func TestProviderTestBadKeyRefusedWithoutNetwork(t *testing.T) {
	isolateProviderHome(t)
	const secret = "sk-bad\nkey"
	t.Setenv("FF_TEST_CMD_BAD_KEY", secret)
	// No test server: Load resolves the active provider, so a bad
	// credential fails fast at startup without ever dialing.
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: http://127.0.0.1:9/v1\n    api_key_env: FF_TEST_CMD_BAD_KEY\n")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() accepted a CRLF key")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sk-bad") {
		t.Fatalf("error leaked the key: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "FF_TEST_CMD_BAD_KEY") {
		t.Errorf("error = %q, want it to name the variable", err.Error())
	}
}

func TestProbeAuthHeaderRejectsBadKey(t *testing.T) {
	isolateProviderHome(t)
	t.Setenv("FF_TEST_PROBE_BAD_KEY", "bad\r\nkey")
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: http://127.0.0.1:9/v1\n    api_key_env: FF_TEST_PROBE_BAD_KEY\n")

	// Load itself must already fail, so the probe path is unreachable;
	// probeAuthHeader must also refuse a programmatically built bad key
	// without sending anything.
	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted a CRLF key")
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:9/models", nil)
	bad := config.ResolvedProvider{ID: "custom", AuthEnvVar: "FF_TEST_PROBE_BAD_KEY", APIKey: "bad\r\nkey"}
	if err := probeAuthHeader(bad, req); err == nil {
		t.Fatal("probeAuthHeader accepted a CRLF key")
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty (nothing sent)", got)
	}
}

func TestProviderTestMetadataOptInPassesGate(t *testing.T) {
	isolateProviderHome(t)
	// With the opt-in, the metadata gate itself must pass. Resolution and
	// spec validation (no network) prove the gate; the live probe is not
	// attempted here because it would dial a real metadata address.
	writeProviderConfig(t, "model:\n  provider: meta\n  name: m\nproviders:\n  meta:\n    type: openai-compatible\n    base_url: http://169.254.169.254/v1\n    allow_metadata_endpoint: true\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() with opt-in error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("meta", "m")
	if err != nil {
		t.Fatalf("ResolveProvider with opt-in error = %v", err)
	}
	if err := resolved.Spec("m").Validate(); err != nil {
		t.Fatalf("Spec.Validate() with opt-in error = %v", err)
	}
}
