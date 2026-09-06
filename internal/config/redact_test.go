package config

import (
	"strings"
	"testing"

	"forcefield/internal/redact"
)

// TestResolveEnvValueRegistersSecret pins that a resolved credential is
// registered for exact-value redaction everywhere the centralized scrub
// runs (tool output, errors, diagnostics).
func TestResolveEnvValueRegistersSecret(t *testing.T) {
	redact.ResetSecrets()
	t.Cleanup(redact.ResetSecrets)
	const value = "test-only-env-secret-value-999"
	t.Setenv("FORCEFIELD_TEST_API_KEY_XYZ", value)

	got, source, err := ResolveEnvValue("FORCEFIELD_TEST_API_KEY_XYZ")
	if err != nil {
		t.Fatalf("ResolveEnvValue: %v", err)
	}
	if got != value || source != "environment" {
		t.Fatalf("got %q from %q", got, source)
	}
	if out := redact.Scrub("server echoed " + value + " back"); strings.Contains(out, value) {
		t.Errorf("registered value survived scrub: %q", out)
	}
}
