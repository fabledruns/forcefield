package config

import (
	"sort"

	"forcefield/internal/providers"
)

// CredentialEnvNames returns the distinct environment variable names
// Forcefield itself reads as provider credentials for cfg. The legacy
// key is always included. Every referenced provider contributes its
// api_key_env override, falling back to the aliased service preset's
// default variable exactly as ResolveProvider derives it: explicit
// providers entries, the active model provider, and any agent provider
// hint (an agent switch resolves that provider at runtime, so its key
// must already be listed). A preset-only reference with no providers
// entry still names its key.
//
// The result feeds sandbox Policy.CredentialEnv on the runtime path so
// native shell/job children do not inherit these values. It never
// includes values, only names, and it never errors: unknown provider
// ids simply contribute their explicit api_key_env (or nothing), since
// stripping is best-effort hygiene and resolution errors belong to the
// provider path, not to child-environment construction.
func CredentialEnvNames(cfg *Config) []string {
	out := []string{apiKeyName}
	seen := map[string]bool{apiKeyName: true}
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	if cfg == nil {
		return out
	}
	ids := make([]string, 0, len(cfg.Providers)+len(cfg.Agents)+1)
	for id := range cfg.Providers {
		ids = append(ids, id)
	}
	if cfg.Model.Provider != "" {
		ids = append(ids, cfg.Model.Provider)
	}
	for _, agent := range cfg.Agents {
		if agent.Provider != "" {
			ids = append(ids, agent.Provider)
		}
	}
	sort.Strings(ids)

	done := make(map[string]bool)
	for _, id := range ids {
		if id == "" || done[id] {
			continue
		}
		done[id] = true
		entry := cfg.Providers[id]
		add(entry.APIKeyEnv)
		service := providers.Preset{}
		if preset, ok := providers.PresetByID(id); ok {
			service = preset
		} else if entry.Type != "" {
			if aliased, ok := providers.PresetByID(entry.Type); ok {
				service = aliased
			}
		}
		add(service.AuthEnvVar)
	}
	return out
}
