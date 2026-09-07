package hardening

import (
	"strings"
	"testing"

	"forcefield/internal/redact"
)

// P1.15 lab: secret scrubbing across nested representations.
// Reproduction for RC5 FF-100-H04: ScrubMap was shallow (top-level strings
// only); nested map/slice secrets persisted raw in tool_calls.arguments.env.
func TestScrubNestedMapsSlices(t *testing.T) {
	redact.ResetSecrets()
	secret := "hf-deterministic-test-secret-0123456789"
	redact.AddSecret(secret)
	defer redact.ResetSecrets()

	args := map[string]any{
		"command": "echo hi",
		"env": map[string]any{
			"HF_TOKEN": secret,
			"nested": map[string]any{
				"key": secret,
			},
			"list": []any{secret, "safe"},
		},
	}
	got := redact.ScrubMap(args)
	env, ok := got["env"].(map[string]any)
	if !ok {
		t.Fatalf("env map lost in ScrubMap: %#v", got["env"])
	}
	if s, _ := env["HF_TOKEN"].(string); strings.Contains(s, secret) {
		t.Fatalf("nested env secret leaked: %q", s)
	}
	nested, _ := env["nested"].(map[string]any)
	if s, _ := nested["key"].(string); strings.Contains(s, secret) {
		t.Fatalf("doubly-nested secret leaked: %q", s)
	}
	lst, _ := env["list"].([]any)
	if s, _ := lst[0].(string); strings.Contains(s, secret) {
		t.Fatalf("slice secret leaked: %q", s)
	}
	// Top-level still scrubbed.
	args2 := map[string]any{"command": "use " + secret}
	if s := redact.ScrubMap(args2)["command"].(string); strings.Contains(s, secret) {
		t.Fatalf("top-level secret leaked: %q", s)
	}
	// Original map must not be mutated.
	if m := args["env"].(map[string]any); m["HF_TOKEN"] != secret {
		t.Fatalf("ScrubMap mutated input")
	}
}

func TestScrubJSONAndErrorSurfaces(t *testing.T) {
	redact.ResetSecrets()
	secret := "hf-json-test-secret-9876543210"
	redact.AddSecret(secret)
	defer redact.ResetSecrets()

	payload := `{"token": "` + secret + `", "nested": {"k": "` + secret + `"}}`
	if out := redact.Scrub(payload); strings.Contains(out, secret) {
		t.Fatalf("JSON secret leaked: %q", out)
	}
}
