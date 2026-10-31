package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
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
	// ChatPath is the configured chat-completions path override ("" keeps
	// the default "/chat/completions").
	ChatPath string
	// AuthMode selects how the API key is sent (see providers.Spec).
	AuthMode string
	// AuthHeader names the custom header for AuthMode "header".
	AuthHeader string
	// AuthScheme optionally overrides the credential prefix.
	AuthScheme string
	// TimeoutSeconds overrides the response-header timeout (0 = default).
	TimeoutSeconds int
	// Project is the Google Cloud project ID (Vertex AI).
	Project string
	// Location is the cloud region (Vertex AI location).
	Location string
	// CredentialsFile optionally points at a service-account JSON key
	// used only for this provider (Vertex AI).
	CredentialsFile string
	// Region is the AWS region (Bedrock).
	Region string
	// Profile selects an AWS shared-config profile (Bedrock).
	Profile string
	// APIVersion pins the Azure data-plane api-version query parameter
	// (empty keeps the adapter's default).
	APIVersion string
	// AllowMetadataEndpoint explicitly opts in to a metadata-service URL.
	AllowMetadataEndpoint bool
	// CapabilitiesOverride optionally forces capability flags.
	CapabilitiesOverride *providers.CapabilitiesOverride
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
		ID:                    r.ID,
		Type:                  r.Type,
		Label:                 r.Label,
		BaseURL:               strings.TrimRight(r.BaseURL, "/"),
		APIKey:                r.APIKey,
		Model:                 model,
		Headers:               headers,
		ChatPath:              r.ChatPath,
		AuthMode:              r.AuthMode,
		AuthHeader:            r.AuthHeader,
		AuthScheme:            r.AuthScheme,
		TimeoutSeconds:        r.TimeoutSeconds,
		Project:               r.Project,
		Location:              r.Location,
		CredentialsFile:       r.CredentialsFile,
		Region:                r.Region,
		Profile:               r.Profile,
		APIVersion:            r.APIVersion,
		CapabilitiesOverride:  r.CapabilitiesOverride,
		AllowMetadataEndpoint: r.AllowMetadataEndpoint,
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

	// Bedrock resolves its AWS identity here: region is required (with
	// standard env fallbacks), long-lived keys are rejected (the SDK
	// chain owns credentials), and the endpoint stays SDK-resolved
	// unless base_url overrides it (e.g. VPC endpoints).
	isBedrock := protocol == "bedrock"
	region, profile := "", ""
	if isBedrock {
		region = strings.TrimSpace(firstNonEmpty(entry.Region, os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION")))
		profile = strings.TrimSpace(entry.Profile)
		if region == "" {
			return ResolvedProvider{}, &SetupNeededError{fmt.Errorf("provider %q needs a region - set providers.%s.region (or AWS_REGION)", id, id)}
		}
		if entry.APIKeyEnv != "" {
			return ResolvedProvider{}, fmt.Errorf("provider %q uses the standard AWS credential chain, not API keys - remove providers.%s.api_key_env and configure AWS credentials instead (environment, shared profile, or IAM role)", id, id)
		}
	}
	isVertex := protocol == "vertex"
	project, location, credentialsFile := "", "", ""
	if isVertex {
		project = strings.TrimSpace(firstNonEmpty(entry.Project, os.Getenv("GOOGLE_CLOUD_PROJECT")))
		location = strings.TrimSpace(entry.Location)
		credentialsFile = strings.TrimSpace(entry.CredentialsFile)
		if project == "" {
			return ResolvedProvider{}, &SetupNeededError{fmt.Errorf("provider %q needs a Google Cloud project - set providers.%s.project (or GOOGLE_CLOUD_PROJECT)", id, id)}
		}
		if location == "" {
			return ResolvedProvider{}, &SetupNeededError{fmt.Errorf("provider %q needs a location - set providers.%s.location (e.g. us-central1)", id, id)}
		}
		if entry.APIKeyEnv != "" {
			return ResolvedProvider{}, fmt.Errorf("provider %q uses Application Default Credentials, not API keys - remove providers.%s.api_key_env and configure ADC instead (`gcloud auth application-default login` or GOOGLE_APPLICATION_CREDENTIALS)", id, id)
		}
		if credentialsFile != "" {
			if info, err := os.Stat(credentialsFile); err != nil || info.IsDir() {
				return ResolvedProvider{}, fmt.Errorf("provider %q credentials_file %q is not readable", id, credentialsFile)
			}
		}
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
	if baseURL == "" && isVertex {
		baseURL = "https://" + location + "-aiplatform.googleapis.com"
	}
	if baseURL == "" && protocol != "bedrock" {
		return ResolvedProvider{}, &SetupNeededError{fmt.Errorf(
			"provider %q has no base_url - set base_url under providers.%s (or model.endpoint)",
			id, id,
		)}
	}
	if baseURL != "" {
		if err := validateBaseURL(id, baseURL); err != nil {
			return ResolvedProvider{}, err
		}
	}

	envVar := firstNonEmpty(entry.APIKeyEnv, service.AuthEnvVar)
	authRequired := service.Auth == providers.AuthRequired || entry.APIKeyEnv != ""
	apiKey, apiKeySource := "", ""
	if envVar != "" {
		value, source, err := ResolveEnvValue(envVar)
		if err != nil {
			return ResolvedProvider{}, err
		}
		if value != "" && !providers.IsValidAPIKey(value) {
			return ResolvedProvider{}, fmt.Errorf("provider %q: value in %s contains invalid control characters (check for stray line breaks) and will not be used", id, envVar)
		}
		apiKey, apiKeySource = value, source
	}

	if providers.IsMetadataHost(providers.EndpointHost(baseURL)) && !entry.AllowMetadataEndpoint {
		return ResolvedProvider{}, fmt.Errorf("provider %q base_url points at a cloud metadata endpoint (%s) - refusing to send requests there (set allow_metadata_endpoint: true only if you intentionally run a model server there)", id, providers.EndpointHost(baseURL))
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
		ID:                    id,
		Type:                  protocol,
		Label:                 label,
		BaseURL:               baseURL,
		APIKey:                apiKey,
		APIKeySource:          apiKeySource,
		AuthRequired:          authRequired,
		AuthEnvVar:            envVar,
		Model:                 entry.Model,
		Headers:               headers,
		Models:                models,
		ChatPath:              entry.APIPath,
		AuthMode:              entry.AuthMode,
		AuthHeader:            entry.AuthHeader,
		AuthScheme:            entry.AuthScheme,
		TimeoutSeconds:        entry.TimeoutSeconds,
		Project:               project,
		Location:              location,
		CredentialsFile:       credentialsFile,
		Region:                region,
		Profile:               profile,
		APIVersion:            strings.TrimSpace(entry.APIVersion),
		CapabilitiesOverride:  entry.Capabilities,
		AllowMetadataEndpoint: entry.AllowMetadataEndpoint,
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

// SetupNeededError marks a resolution failure caused by a missing
// per-user identity or resource setting - Bedrock's region, Vertex's
// project or location, an Azure resource URL - rather than by a
// malformed providers entry. These fields cannot live in the shared
// catalog preset, so the fix is always user configuration. Picker
// surfaces show these as "setup needed" rows carrying the embedded
// guidance; every other failure renders as generic unavailability so a
// typo is never presented as pending setup.
type SetupNeededError struct{ Err error }

func (e *SetupNeededError) Error() string { return e.Err.Error() }
func (e *SetupNeededError) Unwrap() error { return e.Err }

// IsSetupNeeded reports whether err stems from a missing required
// setup field during provider resolution.
func IsSetupNeeded(err error) bool {
	var se *SetupNeededError
	return errors.As(err, &se)
}

// ValidateProviderEntry checks one providers entry's structure without
// touching secrets or the network. It is the same validation Load
// applies to every entry, exported so mutating commands (ff provider
// add) can refuse to save an entry that would make the config
// unloadable.
func ValidateProviderEntry(id string, p ProviderConfig) error {
	return validateEntry(id, p)
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
	for name, value := range p.Headers {
		if !isValidHeaderName(name) {
			return fmt.Errorf("providers.%s has an invalid header name %q", id, name)
		}
		if isReservedAuthHeader(name) {
			return fmt.Errorf("providers.%s header %q is reserved for authentication and would silently override the resolved API key - remove it and provide the key via api_key_env instead", id, name)
		}
		if !providers.IsValidHeaderValue(value) {
			return fmt.Errorf("providers.%s header %q value contains invalid control characters and will not be sent", id, name)
		}
	}
	for i, m := range p.Models {
		if strings.TrimSpace(m) == "" {
			return fmt.Errorf("providers.%s.models[%d] is empty", id, i)
		}
	}
	if err := validateAPIPath(id, p.APIPath); err != nil {
		return err
	}
	if err := validateAuthMode(id, p); err != nil {
		return err
	}
	if p.TimeoutSeconds < 0 || p.TimeoutSeconds > 300 {
		return fmt.Errorf("providers.%s.timeout_seconds %d is out of range (0 keeps the default, else 1-300)", id, p.TimeoutSeconds)
	}
	if err := validateVertexFields(id, p); err != nil {
		return err
	}
	if err := validateBedrockFields(id, p); err != nil {
		return err
	}
	if err := validateAzureFields(id, p); err != nil {
		return err
	}
	if p.BaseURL != "" && providers.IsMetadataHost(providers.EndpointHost(p.BaseURL)) && !p.AllowMetadataEndpoint {
		return fmt.Errorf("providers.%s.base_url points at a cloud metadata endpoint (%s) - refusing to send requests there (set allow_metadata_endpoint: true only if you intentionally run a model server there)", id, providers.EndpointHost(p.BaseURL))
	}
	return nil
}

// validateVertexFields checks Vertex AI cloud-identity fields. Project
// may come from GOOGLE_CLOUD_PROJECT at resolve time so it is not
// required here; location has no env fallback and is required. API keys
// and OpenAI-transport knobs are rejected for vertex so a misconfigured
// entry fails fast instead of being silently ignored.
func validateVertexFields(id string, p ProviderConfig) error {
	isVertex := p.Type == "vertex" || (p.Type == "" && id == "vertex")
	if !isVertex {
		if p.Project != "" || p.Location != "" || p.CredentialsFile != "" {
			return fmt.Errorf("providers.%s.project/location/credentials_file are only supported for type \"vertex\"", id)
		}
		return nil
	}
	if strings.TrimSpace(p.Location) == "" {
		return fmt.Errorf("providers.%s.location is required (e.g. us-central1)", id)
	}
	if p.APIKeyEnv != "" {
		return fmt.Errorf("providers.%s uses Application Default Credentials, not API keys - remove api_key_env and configure ADC instead", id)
	}
	if p.AuthMode != "" || p.AuthHeader != "" || p.AuthScheme != "" {
		return fmt.Errorf("providers.%s authenticates with Application Default Credentials - auth_mode/auth_header/auth_scheme do not apply to type \"vertex\"", id)
	}
	if p.APIPath != "" {
		return fmt.Errorf("providers.%s.api_path does not apply to type \"vertex\" - the resource path is derived from project/location", id)
	}
	return nil
}

// validateBedrockFields checks Bedrock identity fields. Region may come
// from AWS_REGION/AWS_DEFAULT_REGION at resolve time so it is not
// required here. API keys and OpenAI-transport knobs are rejected so a
// misconfigured entry fails fast instead of being silently ignored.
func validateBedrockFields(id string, p ProviderConfig) error {
	isBedrock := p.Type == "bedrock" || (p.Type == "" && id == "bedrock")
	if !isBedrock {
		if p.Region != "" || p.Profile != "" {
			return fmt.Errorf("providers.%s.region/profile are only supported for type \"bedrock\"", id)
		}
		return nil
	}
	if p.APIKeyEnv != "" {
		return fmt.Errorf("providers.%s uses the standard AWS credential chain, not API keys - remove api_key_env and configure AWS credentials instead", id)
	}
	if p.AuthMode != "" || p.AuthHeader != "" || p.AuthScheme != "" {
		return fmt.Errorf("providers.%s authenticates with AWS credentials (SigV4) - auth_mode/auth_header/auth_scheme do not apply to type \"bedrock\"", id)
	}
	if p.APIPath != "" {
		return fmt.Errorf("providers.%s.api_path does not apply to type \"bedrock\" - the Converse API paths are fixed", id)
	}
	return nil
}

// validateAzureFields checks Azure resource fields. The deployment name
// is the configured model ID (no dedicated field) and the resource
// endpoint must come from base_url. Key authentication uses Azure's
// fixed api-key header, so the OpenAI-transport auth knobs are rejected
// instead of being silently ignored; Entra tokens travel as
// auth_mode: bearer from an env-var token.
func validateAzureFields(id string, p ProviderConfig) error {
	isAzure := p.Type == "azure" || (p.Type == "" && id == "azure")
	if !isAzure {
		if p.APIVersion != "" {
			return fmt.Errorf("providers.%s.api_version is only supported for type \"azure\"", id)
		}
		return nil
	}
	if p.APIPath != "" {
		return fmt.Errorf("providers.%s.api_path does not apply to type \"azure\" - the deployment path is derived from the model name", id)
	}
	if p.AuthHeader != "" || p.AuthScheme != "" {
		return fmt.Errorf("providers.%s authenticates with Azure's fixed api-key header or an Entra bearer token - auth_header/auth_scheme do not apply to type \"azure\"", id)
	}
	switch strings.ToLower(strings.TrimSpace(p.AuthMode)) {
	case "", "bearer", "none":
	default:
		return fmt.Errorf("providers.%s auth_mode %q is not supported for azure (omit it for api-key auth, or use \"bearer\" for a Microsoft Entra ID token from api_key_env)", id, p.AuthMode)
	}
	if err := validateAPIVersion(id, p.APIVersion); err != nil {
		return err
	}
	return nil
}

// validateAPIVersion checks the Azure api-version charset without
// touching the network. Empty keeps the adapter's default; the allowed
// characters also prevent query-parameter injection.
func validateAPIVersion(id, v string) error {
	if v == "" {
		return nil
	}
	if len(v) > 64 {
		return fmt.Errorf("providers.%s.api_version %q is too long (max 64 characters)", id, v)
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("providers.%s.api_version %q may only contain letters, digits, \".\", \"_\", and \"-\"", id, v)
		}
	}
	return nil
}

// validateAPIPath checks an optional chat-path override without touching
// the network. Empty keeps the default "/chat/completions".
func validateAPIPath(id, p string) error {
	if p == "" {
		return nil
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("providers.%s.api_path %q must start with \"/\"", id, p)
	}
	if len(p) < 2 {
		return fmt.Errorf("providers.%s.api_path %q is too short", id, p)
	}
	if strings.ContainsAny(p, " \t\r\n\\?#") {
		return fmt.Errorf("providers.%s.api_path %q must be a plain absolute path without spaces, query, or fragment", id, p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("providers.%s.api_path %q must not contain \"..\"", id, p)
		}
	}
	return nil
}

// validateAuthMode checks the custom authentication fields. Empty mode
// preserves historic behavior (bearer when a key resolves, none
// otherwise) so existing configs keep working unchanged.
func validateAuthMode(id string, p ProviderConfig) error {
	mode := strings.ToLower(strings.TrimSpace(p.AuthMode))
	switch mode {
	case "", "bearer", "none", "header":
	default:
		return fmt.Errorf("providers.%s.auth_mode %q is not supported (supported: \"bearer\", \"none\", \"header\")", id, p.AuthMode)
	}
	if p.AuthHeader != "" && mode != "header" {
		return fmt.Errorf("providers.%s.auth_header requires auth_mode \"header\"", id)
	}
	if mode == "header" {
		if strings.TrimSpace(p.AuthHeader) == "" {
			return fmt.Errorf("providers.%s.auth_mode \"header\" requires auth_header", id)
		}
		if !isValidHeaderName(p.AuthHeader) {
			return fmt.Errorf("providers.%s has an invalid auth_header name %q", id, p.AuthHeader)
		}
		if isReservedAuthHeader(p.AuthHeader) {
			return fmt.Errorf("providers.%s auth_header %q is reserved for built-in authentication", id, p.AuthHeader)
		}
		for name := range p.Headers {
			if strings.EqualFold(name, p.AuthHeader) {
				return fmt.Errorf("providers.%s header %q collides with auth_header - remove it from headers", id, name)
			}
		}
	} else {
		// Bearer/auto modes own Authorization; a headers entry would
		// silently override the resolved key.
		for name := range p.Headers {
			if strings.EqualFold(name, "authorization") {
				return fmt.Errorf("providers.%s header %q is reserved for authentication and would silently override the resolved API key - remove it and provide the key via api_key_env instead", id, name)
			}
		}
	}
	if p.AuthScheme != "" {
		if mode != "bearer" && mode != "header" {
			return fmt.Errorf("providers.%s.auth_scheme requires auth_mode \"bearer\" or \"header\"", id)
		}
		if !isValidHeaderName(p.AuthScheme) {
			return fmt.Errorf("providers.%s.auth_scheme %q is not a valid auth scheme", id, p.AuthScheme)
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
	if strings.ContainsAny(raw, "?#") {
		return fmt.Errorf("providers.%s.base_url %q must not contain a query string or fragment - base_url is a plain API root and adapters append the documented API paths", id, raw)
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

// reservedAuthHeaders are request headers the provider adapters set
// from the resolved credential (Authorization Bearer, x-api-key,
// x-goog-api-key). A custom header with one of these names - in any
// casing, since HTTP header names are case-insensitive - would silently
// replace the API key on every request, and would persist a live
// credential in config.yaml, defeating env-only secrets.
var reservedAuthHeaders = []string{"authorization", "x-api-key", "x-goog-api-key", "api-key"}

// isReservedAuthHeader reports whether name collides
// case-insensitively with an authentication header.
func isReservedAuthHeader(name string) bool {
	lower := strings.ToLower(name)
	for _, h := range reservedAuthHeaders {
		if lower == h {
			return true
		}
	}
	return false
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
