package config

import (
	"testing"
)

// Setup-needed classification is the contract the provider picker
// relies on: missing per-user identity or resource fields (region,
// project, location, resource URL) surface as setup rows, and every
// other resolution failure must not be presented as pending setup.
func TestResolveProviderSetupFailuresAreTyped(t *testing.T) {
	// Clear the identity env fallbacks the resolver consults so the
	// missing-field paths are taken regardless of the host environment.
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	// The full resolver message is pinned: wrapping may not change the
	// text, because pickers and doctor render it verbatim.
	cases := []struct {
		name    string
		id      string
		entry   ProviderConfig
		wantMsg string
	}{
		{
			name:    "vertex needs project",
			id:      "vertex",
			entry:   ProviderConfig{Type: "vertex", Location: "us-central1"},
			wantMsg: `provider "vertex" needs a Google Cloud project - set providers.vertex.project (or GOOGLE_CLOUD_PROJECT)`,
		},
		{
			name:    "vertex needs location",
			id:      "vertex",
			entry:   ProviderConfig{Type: "vertex", Project: "my-proj"},
			wantMsg: `provider "vertex" needs a location - set providers.vertex.location (e.g. us-central1)`,
		},
		{
			name:    "bedrock needs region",
			id:      "bedrock",
			entry:   ProviderConfig{Type: "bedrock"},
			wantMsg: `provider "bedrock" needs a region - set providers.bedrock.region (or AWS_REGION)`,
		},
		{
			name:    "azure needs base_url",
			id:      "azure",
			entry:   ProviderConfig{Type: "azure"},
			wantMsg: `provider "azure" has no base_url - set base_url under providers.azure (or model.endpoint)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Model:     Model{Provider: "ollama", Name: "m"},
				Providers: map[string]ProviderConfig{tc.id: tc.entry},
			}
			_, err := cfg.ResolveProvider(tc.id, "")
			if err == nil {
				t.Fatal("ResolveProvider() = nil error, want a setup failure")
			}
			if err.Error() != tc.wantMsg {
				t.Errorf("message = %q, want unchanged contract text %q", err.Error(), tc.wantMsg)
			}
			if !IsSetupNeeded(err) {
				t.Errorf("IsSetupNeeded(%v) = false, want true", err)
			}
		})
	}
}

func TestResolveProviderMisconfigurationIsNotTypedAsSetup(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	cases := []struct {
		name  string
		id    string
		entry ProviderConfig
	}{
		{"metadata endpoint refused", "meta", ProviderConfig{Type: "openai-compatible", BaseURL: "http://169.254.169.254/latest/meta-data"}},
		{"unreadable credentials file", "vb", ProviderConfig{Type: "vertex", Project: "p", Location: "us-central1", CredentialsFile: "definitely-missing.json"}},
		{"bedrock api_key_env rejected", "bk", ProviderConfig{Type: "bedrock", Region: "us-east-1", APIKeyEnv: "KEY"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Model:     Model{Provider: "ollama", Name: "m"},
				Providers: map[string]ProviderConfig{tc.id: tc.entry},
			}
			_, err := cfg.ResolveProvider(tc.id, "")
			if err == nil {
				t.Fatalf("ResolveProvider(%q) = nil error, want a resolution failure", tc.id)
			}
			if IsSetupNeeded(err) {
				t.Errorf("IsSetupNeeded(%v) = true, want false for a configuration mistake", err)
			}
		})
	}
}
