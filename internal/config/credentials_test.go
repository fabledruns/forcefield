package config

import (
	"testing"
)

func credentialSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

func TestCredentialEnvNamesAlwaysIncludesLegacy(t *testing.T) {
	for _, cfg := range []*Config{nil, {}, {Model: Model{Provider: "ollama"}}} {
		got := credentialSet(CredentialEnvNames(cfg))
		if !got["NVIDIA_API_KEY"] {
			t.Errorf("CredentialEnvNames(%+v) missing legacy NVIDIA_API_KEY: %v", cfg, got)
		}
	}
}

func TestCredentialEnvNamesCustomAPIKeyEnv(t *testing.T) {
	cfg := &Config{
		Model: Model{Provider: "work"},
		Providers: map[string]ProviderConfig{
			"work": {Type: "openai", APIKeyEnv: "WORK_OPENAI_KEY"},
		},
	}
	got := credentialSet(CredentialEnvNames(cfg))
	for _, want := range []string{"NVIDIA_API_KEY", "WORK_OPENAI_KEY", "OPENAI_API_KEY"} {
		if !got[want] {
			t.Errorf("CredentialEnvNames missing %q: %v", want, CredentialEnvNames(cfg))
		}
	}
}

func TestCredentialEnvNamesPresetWithoutEntry(t *testing.T) {
	cfg := &Config{Model: Model{Provider: "anthropic"}}
	got := credentialSet(CredentialEnvNames(cfg))
	if !got["ANTHROPIC_API_KEY"] {
		t.Errorf("preset-only config missing ANTHROPIC_API_KEY: %v", CredentialEnvNames(cfg))
	}
}

func TestCredentialEnvNamesDistinct(t *testing.T) {
	cfg := &Config{
		Model: Model{Provider: "nvidia"},
		Providers: map[string]ProviderConfig{
			"nvidia": {},
			"extra":  {Type: "nvidia", APIKeyEnv: "NVIDIA_API_KEY"},
		},
	}
	got := CredentialEnvNames(cfg)
	seen := make(map[string]int)
	for _, n := range got {
		seen[n]++
		if seen[n] > 1 {
			t.Errorf("CredentialEnvNames has duplicate %q: %v", n, got)
		}
		if n == "" {
			t.Errorf("CredentialEnvNames has empty entry: %v", got)
		}
	}
}

// TestCredentialEnvNamesAgentProviderHint covers the agent-switch path:
// an agent naming a provider that has no providers entry and is not the
// active model provider still contributes its preset key, because
// switching to that agent resolves the provider at runtime. The same
// provider referenced three ways (entry alias, agent hint, model) must
// still yield one name.
func TestCredentialEnvNamesAgentProviderHint(t *testing.T) {
	cfg := &Config{
		Model: Model{Provider: "ollama"},
		Agents: map[string]AgentConfig{
			"coding": {Provider: "anthropic"},
		},
	}
	got := credentialSet(CredentialEnvNames(cfg))
	if !got["ANTHROPIC_API_KEY"] {
		t.Errorf("agent provider hint missing ANTHROPIC_API_KEY: %v", CredentialEnvNames(cfg))
	}

	dup := &Config{
		Model: Model{Provider: "anthropic"},
		Providers: map[string]ProviderConfig{
			"work": {Type: "anthropic"},
		},
		Agents: map[string]AgentConfig{
			"coding":  {Provider: "anthropic"},
			"general": {},
		},
	}
	names := CredentialEnvNames(dup)
	count := 0
	for _, n := range names {
		if n == "ANTHROPIC_API_KEY" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("ANTHROPIC_API_KEY appears %d times, want exactly once: %v", count, names)
	}
}
