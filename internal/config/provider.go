package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"forcefield/internal/providers"
)

// ResolvedProvider is the fully-resolved, ready-to-use form of one
// configured provider: configuration entries merged over catalog
// defaults, with secrets looked up. It is built per use and never
// persisted or logged.
type ResolvedProvider struct {
	// ID is the configured provider key ("ollama", "openai", "local", …).
	ID string
	// Type is the wire protocol serving this provider (one of
	// providers.ProtocolTypes()).
	Type string
	// Label is the human-facing service name used in errors.
	Label string
	// BaseURL is the API root without trailing slash.
	BaseURL string
	// APIKey authenticates requests; empty when none is configured.
	APIKey string
	// APIKeySource describes where the key came from ("environment",
	// ".env file …", or "" when absent). It never contains the value.
	APIKeySource string
	// AuthRequired reports whether the service cannot work without a key.
	AuthRequired bool
	// AuthEnvVar names the environment variable (or .env key) the API key
	// is read from; empty for unauthenticated services.
	AuthEnvVar string
	// Model is the default model recorded for this provider (may be empty;
	// the active model always comes from model.name).
	Model string
	// Headers are extra HTTP headers for every request.
	Headers map[string]string
	// Models lists configured model IDs for providers that cannot
	// enumerate their own.
	Models []string
}

// Spec converts the resolved provider into the form adapters consume.
// activeModel is the model ID requests should use (model.name).
func (r ResolvedProvider) Spec(activeModel string) providers.Spec {
	headers := make(map[string]string, len(r.Headers))
	for k, v := range r.Headers {
		headers[k] = v
	}
	model := activeModel
	if model == "" {
		model = r.Model
	}
	return providers.Spec{
		ID:      r.ID,
		Type:    r.Type,
		Label:   r.Label,
		BaseURL: strings.TrimRight(r.BaseURL, "/"),
		APIKey:  r.APIKey,
		Model:   model,
		Headers: headers,
	}
}

// ResolveProvider merges configuration, catalog defaults, and legacy
// top-level fields into one concrete provider description.
//
// Precedence per field: explicit entry value → catalog preset for the
// provider id (or for an aliased type like "type: openai") → legacy
// model.* fields when resolving the active provider.
//
// A missing API key is not an error here; callers decide whether the
// intended use requires one (AuthRequired says what the service needs).
func (c *Config) ResolveProvider(id, activeModel string) (ResolvedProvider, error) {
	if strings.TrimSpace(id) == "" {
		return ResolvedProvider{}, fmt.Errorf("provider id cannot be empty")
	}

	entry, hasEntry := c.Providers[id]
	preset, isPreset := providers.PresetByID(id)

	if !hasEntry && !isPreset {
		return ResolvedProvider{}, fmt.Errorf(
			"provider %q is not configured - add a providers.%s section to config.yaml",
			id, id,
		)
	}

	// A custom provider id can alias a known service via its type
	// (e.g. id "work-nim" with "type: nvidia"); that service then
	// supplies defaults exactly as if it were the id itself.
	service := preset
	if !isPreset && entry.Type != "" {
		if aliased, ok := providers.PresetByID(entry.Type); ok {
			service = aliased
		}
	}

	protocol := entry.Type
	if service.ID != "" {
		protocol = service.Type
	}
	if protocol == "" {
		return ResolvedProvider{}, fmt.Errorf(
			"provider %q must declare a type - supported: %s",
			id, strings.Join(providers.KnownTypes(), ", "),
		)
	}
	if !providers.IsKnownType(protocol) {
		return ResolvedProvider{}, fmt.Errorf(
			"providers.%s.type %q is not supported - supported: %s",
			id, entry.Type, strings.Join(providers.KnownTypes(), ", "),
		)
	}

	label := id
	switch {
	case isPreset:
		label = preset.Name
	case service.ID != "":
		label = service.Name
	}

	baseURL := firstNonEmpty(entry.BaseURL, service.BaseURL)
	if baseURL == "" && id == c.Model.Provider {
		baseURL = c.Model.Endpoint // legacy single-provider field
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return ResolvedProvider{}, fmt.Errorf(
			"provider %q has no base_url - set base_url under providers.%s (or model.endpoint)",
			id, id,
		)
	}
	if err := validateBaseURL(id, baseURL); err != nil {
		return ResolvedProvider{}, err
	}

	envVar := firstNonEmpty(entry.APIKeyEnv, service.AuthEnvVar)
	authRequired := service.Auth == providers.AuthRequired || entry.APIKeyEnv != ""
	apiKey, apiKeySource := "", ""
	if envVar != "" {
		value, source, err := ResolveEnvValue(envVar)
		if err != nil {
			return ResolvedProvider{}, err
		}
		apiKey, apiKeySource = value, source
	}

	headers := make(map[string]string, len(entry.Headers))
	for k, v := range entry.Headers {
		headers[k] = v
	}

	models := append([]string(nil), entry.Models...)
	if len(models) == 0 && service.ID != "" {
		for _, m := range service.Models {
			models = append(models, m.ID)
		}
	}

	return ResolvedProvider{
		ID:           id,
		Type:         protocol,
		Label:        label,
		BaseURL:      baseURL,
		APIKey:       apiKey,
		APIKeySource: apiKeySource,
		AuthRequired: authRequired,
		AuthEnvVar:   envVar,
		Model:        entry.Model,
		Headers:      headers,
		Models:       models,
	}, nil
}

// ResolveAll resolves every configured provider plus every built-in
// catalog service, in catalog display order (custom entries follow,
// sorted by ID), for pickers and status reporting. Providers that fail
// resolution are returned with their error so the UI can explain why
// they're unavailable instead of hiding them.
func (c *Config) ResolveAll(activeModel string) ([]ResolvedProvider, []ProviderError) {
	ids := make(map[string]struct{})
	for id := range c.Providers {
		ids[id] = struct{}{}
	}
	for _, preset := range providers.Catalog {
		delete(ids, preset.ID)
	}

	ordered := make([]string, 0, len(providers.Catalog)+len(ids))
	for _, preset := range providers.Catalog {
		ordered = append(ordered, preset.ID)
	}
	extra := make([]string, 0, len(ids))
	for id := range ids {
		extra = append(extra, id)
	}
	sort.Strings(extra)
	ordered = append(ordered, extra...)

	out := make([]ResolvedProvider, 0, len(ordered))
	var failures []ProviderError
	for _, id := range ordered {
		resolved, err := c.ResolveProvider(id, activeModel)
		if err != nil {
			failures = append(failures, ProviderError{ID: id, Err: err})
			continue
		}
		out = append(out, resolved)
	}
	return out, failures
}

// ProviderError pairs a provider ID with why it could not be resolved.
type ProviderError struct {
	ID  string
	Err error
}

// validateEntry checks one providers entry's structure without touching
// secrets.
func validateEntry(id string, p ProviderConfig) error {
	if p.Type != "" && !providers.IsKnownType(p.Type) {
		return fmt.Errorf(
			"providers.%s.type %q is not supported - supported: %s",
			id, p.Type, strings.Join(providers.KnownTypes(), ", "),
		)
	}
	if p.BaseURL != "" {
		if err := validateBaseURL(id, p.BaseURL); err != nil {
			return err
		}
	}
	if p.APIKeyEnv != "" && !isEnvVarName(p.APIKeyEnv) {
		return fmt.Errorf("providers.%s.api_key_env %q is not a valid environment variable name", id, p.APIKeyEnv)
	}
	for name := range p.Headers {
		if !isValidHeaderName(name) {
			return fmt.Errorf("providers.%s has an invalid header name %q", id, name)
		}
	}
	for i, m := range p.Models {
		if strings.TrimSpace(m) == "" {
			return fmt.Errorf("providers.%s.models[%d] is empty", id, i)
		}
	}
	return nil
}

// validateBaseURL ensures a base URL is absolute http(s).
func validateBaseURL(id, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("providers.%s.base_url %q is not a valid URL: %w", id, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("providers.%s.base_url %q must be an http:// or https:// URL", id, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("providers.%s.base_url %q has no host", id, raw)
	}
	return nil
}

// isEnvVarName reports whether s is a plausible environment variable name.
func isEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// isValidHeaderName reports whether name is a valid HTTP header token.
func isValidHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
