package providers

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Spec is everything an adapter needs to reach one configured provider:
// the resolved, validated form of a config providers entry plus catalog
// defaults. Secrets ride in APIKey; Specs are built per use from config
// and are never persisted or logged.
type Spec struct {
	// ID is the configured provider key (e.g. "ollama", "local", "openai").
	ID string
	// Type is the wire protocol that serves this provider: one of the
	// registry's factory types ("ollama", "openai-compatible",
	// "openai-responses", "anthropic", "gemini", "vertex", "bedrock",
	// "azure"). Service presets normalize to their protocol before
	// construction.
	Type string
	// Label is the human-facing service name used in errors ("LM Studio").
	Label string
	// BaseURL is the API root, without trailing slash.
	BaseURL string
	// APIKey authenticates requests; empty for unauthenticated services.
	APIKey string
	// Model is the active model ID.
	Model string
	// Headers are extra HTTP headers sent with every request. They are
	// configuration-provided and must not carry secrets users don't want
	// on the wire — but Forcefield never logs them either way.
	Headers map[string]string
	// ChatPath overrides the chat-completions path for OpenAI-compatible
	// servers with genuine path differences (default "/chat/completions").
	// Discovery (GET /models) is unchanged.
	ChatPath string
	// AuthMode selects how APIKey is sent: "" (auto: bearer when a key
	// resolves, none otherwise), "bearer", "none", or "header" (custom
	// header via AuthHeader). Empty preserves historic behavior.
	AuthMode string
	// AuthHeader names the custom header for AuthMode "header"
	// (e.g. "X-API-Key"). Must not collide with reserved auth headers.
	AuthHeader string
	// AuthScheme optionally overrides the credential prefix: "Bearer" by
	// default for bearer mode, raw key by default for header mode.
	// Empty keeps the mode default.
	AuthScheme string
	// TimeoutSeconds overrides the response-header timeout (seconds).
	// Zero keeps the built-in default.
	TimeoutSeconds int
	// CapabilitiesOverride optionally forces capability flags for custom
	// endpoints that do not implement every OpenAI feature. Nil keeps
	// the adapter defaults.
	CapabilitiesOverride *CapabilitiesOverride
	// AllowMetadataEndpoint explicitly opts in to using a cloud
	// instance-metadata URL as a provider endpoint. Default false:
	// metadata endpoints are never legitimate model servers, and sending
	// credentials and prompt content there would expose cloud credentials.
	// Only set this if you intentionally run a model server on such an
	// address. Loopback and LAN addresses are unaffected and stay allowed.
	AllowMetadataEndpoint bool
	// Project is the Google Cloud project ID (Vertex AI).
	Project string
	// Location is the cloud region (Vertex AI location, e.g. us-central1).
	Location string
	// Region is the AWS region (Bedrock, e.g. us-east-1).
	Region string
	// Profile selects an AWS shared-config profile (Bedrock). Empty
	// follows the standard chain, including AWS_PROFILE.
	Profile string
	// CredentialsFile optionally points at a service-account JSON key
	// used only for this provider (Vertex AI). Empty follows the
	// standard ADC chain instead.
	CredentialsFile string
	// APIVersion pins the data-plane api-version query parameter for
	// services that version their REST surface this way (Azure, e.g.
	// "2024-06-01"). Empty lets the adapter pick its documented default.
	// Only the azure type uses it; other types reject it at config time.
	APIVersion string
}

// DefaultChatPath is the standard OpenAI chat-completions path.
const DefaultChatPath = "/chat/completions"

// NormalizedAuthMode returns the lower-cased, trimmed auth mode.
func (s Spec) NormalizedAuthMode() string {
	return normalizeAuthMode(s.AuthMode)
}

func normalizeAuthMode(m string) string {
	return strings.ToLower(strings.TrimSpace(m))
}

// EffectiveChatPath returns the configured chat path or the default.
func (s Spec) EffectiveChatPath() string {
	if strings.TrimSpace(s.ChatPath) == "" {
		return DefaultChatPath
	}
	return s.ChatPath
}

// Validate checks a spec's invariants before an adapter is constructed.
func (s Spec) Validate() error {
	if s.Type == "" {
		return fmt.Errorf("provider %q has no type", s.ID)
	}
	// Bedrock endpoints resolve inside the AWS SDK from the region; an
	// explicit base_url is only an override (e.g. VPC endpoints) and is
	// validated for shape when present.
	if s.Type == "bedrock" {
		if s.BaseURL != "" {
			u, err := url.Parse(s.BaseURL)
			if err != nil {
				return fmt.Errorf("provider %q base_url %q is not a valid URL: %w", s.ID, s.BaseURL, err)
			}
			if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("provider %q base_url %q must be an absolute http:// or https:// URL with a host", s.ID, s.BaseURL)
			}
		}
	} else {
		if s.BaseURL == "" {
			return fmt.Errorf("provider %q has no base_url", s.ID)
		}
		u, err := url.Parse(s.BaseURL)
		if err != nil {
			return fmt.Errorf("provider %q base_url %q is not a valid URL: %w", s.ID, s.BaseURL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("provider %q base_url %q must be an http:// or https:// URL", s.ID, s.BaseURL)
		}
		if u.Host == "" {
			return fmt.Errorf("provider %q base_url %q has no host", s.ID, s.BaseURL)
		}
	}
	for name, value := range s.Headers {
		if !isHTTPHeaderName(name) {
			return fmt.Errorf("provider %q has an invalid header name %q", s.ID, name)
		}
		if !IsValidHeaderValue(value) {
			return fmt.Errorf("provider %q header %q value contains invalid control characters and will not be sent", s.ID, name)
		}
	}
	if !IsValidAPIKey(s.APIKey) {
		return invalidAPIKeyError(s.ID, "")
	}
	if metadataBlocked(s.BaseURL, s.AllowMetadataEndpoint) {
		return fmt.Errorf("provider %q base_url points at a cloud metadata endpoint (%s) - refusing to send requests there (set allow_metadata_endpoint: true only if you intentionally run a model server there)", s.ID, EndpointHost(s.BaseURL))
	}
	if err := validateChatPath(s.ID, s.ChatPath); err != nil {
		return err
	}
	if err := validateAPIVersion(s.ID, s.APIVersion); err != nil {
		return err
	}
	mode := normalizeAuthMode(s.AuthMode)
	switch mode {
	case "", "bearer", "none", "header":
	default:
		return fmt.Errorf("provider %q auth_mode %q is not supported (supported: \"bearer\", \"none\", \"header\")", s.ID, s.AuthMode)
	}
	if s.AuthHeader != "" && mode != "header" {
		return fmt.Errorf("provider %q auth_header requires auth_mode \"header\"", s.ID)
	}
	if mode == "header" {
		if strings.TrimSpace(s.AuthHeader) == "" {
			return fmt.Errorf("provider %q auth_mode \"header\" requires auth_header", s.ID)
		}
		if !isHTTPHeaderName(s.AuthHeader) {
			return fmt.Errorf("provider %q has an invalid auth_header name %q", s.ID, s.AuthHeader)
		}
		if isReservedAuthHeaderSpec(s.AuthHeader) {
			return fmt.Errorf("provider %q auth_header %q is reserved for built-in authentication", s.ID, s.AuthHeader)
		}
		for name := range s.Headers {
			if strings.EqualFold(name, s.AuthHeader) {
				return fmt.Errorf("provider %q header %q collides with auth_header - remove it from headers", s.ID, name)
			}
		}
	} else {
		// Bearer/auto modes own the Authorization header; a custom
		// headers entry with that name would silently override the key.
		for name := range s.Headers {
			if strings.EqualFold(name, "authorization") {
				return fmt.Errorf("provider %q header %q is reserved for authentication and would silently override the resolved API key", s.ID, name)
			}
		}
	}
	if s.AuthScheme != "" {
		if mode != "bearer" && mode != "header" {
			return fmt.Errorf("provider %q auth_scheme requires auth_mode \"bearer\" or \"header\"", s.ID)
		}
		if !isHTTPHeaderName(s.AuthScheme) {
			return fmt.Errorf("provider %q auth_scheme %q is not a valid auth scheme", s.ID, s.AuthScheme)
		}
	}
	if s.TimeoutSeconds < 0 || s.TimeoutSeconds > maxProviderTimeoutSeconds {
		return fmt.Errorf("provider %q timeout_seconds %d is out of range (0 keeps the default, else 1-%d)", s.ID, s.TimeoutSeconds, maxProviderTimeoutSeconds)
	}
	return nil
}

// validateChatPath checks an optional chat-path override. Empty keeps the
// default; otherwise it must be an absolute path without query, fragment,
// or traversal segments.
func validateChatPath(id, p string) error {
	if p == "" {
		return nil
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("provider %q api_path %q must start with \"/\"", id, p)
	}
	if len(p) < 2 {
		return fmt.Errorf("provider %q api_path %q is too short", id, p)
	}
	if strings.ContainsAny(p, " \t\r\n\\?#") {
		return fmt.Errorf("provider %q api_path %q must be a plain absolute path without spaces, query, or fragment", id, p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("provider %q api_path %q must not contain \"..\"", id, p)
		}
	}
	return nil
}

// validateAPIVersion checks an optional data-plane api-version (Azure).
// The charset doubles as query-injection prevention: a version
// containing "&" or "=" could smuggle extra parameters onto the request.
func validateAPIVersion(id, v string) error {
	if v == "" {
		return nil
	}
	if len(v) > 64 {
		return fmt.Errorf("provider %q api_version %q is too long (max 64 characters)", id, v)
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("provider %q api_version %q may only contain letters, digits, \".\", \"_\", and \"-\"", id, v)
		}
	}
	return nil
}

// reservedAuthHeadersSpec mirrors the config-layer deny list so Specs
// built programmatically get the same protection.
var reservedAuthHeadersSpec = []string{"authorization", "x-api-key", "x-goog-api-key", "api-key"}

func isReservedAuthHeaderSpec(name string) bool {
	lower := strings.ToLower(name)
	for _, h := range reservedAuthHeadersSpec {
		if lower == h {
			return true
		}
	}
	return false
}

// isHTTPHeaderName reports whether name is a valid HTTP header token.
func isHTTPHeaderName(name string) bool {
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

// Factory constructs a ModelProvider from a resolved spec. Factories are
// pure: they never touch the network or global state.
type Factory func(Spec) (ModelProvider, error)

// FactoryRegistry maps protocol type names to the adapter that speaks
// them. The runtime asks the registry for providers instead of
// constructing them directly, so adding a transport means registering one
// factory — nothing else changes.
type FactoryRegistry struct {
	mu         sync.RWMutex
	factories  map[string]Factory
	registered []string // registration order, for stable listings
}

// NewFactoryRegistry returns an empty registry.
func NewFactoryRegistry() *FactoryRegistry {
	return &FactoryRegistry{factories: make(map[string]Factory)}
}

// Register adds a factory under typeID. Registering the same type twice
// is a programming error and returns an error.
func (r *FactoryRegistry) Register(typeID string, f Factory) error {
	typeID = strings.ToLower(strings.TrimSpace(typeID))
	if typeID == "" {
		return fmt.Errorf("provider type id cannot be empty")
	}
	if f == nil {
		return fmt.Errorf("provider type %q registered with nil factory", typeID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[typeID]; exists {
		return fmt.Errorf("provider type %q is already registered", typeID)
	}
	r.factories[typeID] = f
	r.registered = append(r.registered, typeID)
	return nil
}

// MustRegister registers f or panics; it is meant for package-level
// wiring of built-in adapters only, where a duplicate is a bug.
func (r *FactoryRegistry) MustRegister(typeID string, f Factory) {
	if err := r.Register(typeID, f); err != nil {
		panic(err)
	}
}

// Create builds the provider described by spec using the factory
// registered for spec.Type.
func (r *FactoryRegistry) Create(spec Spec) (ModelProvider, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	f, ok := r.factories[strings.ToLower(spec.Type)]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf(
			"unsupported provider type %q (supported: %s)",
			spec.Type, strings.Join(r.Types(), ", "),
		)
	}
	p, err := f(spec)
	if err != nil {
		return nil, fmt.Errorf("create %s provider %q: %w", spec.Type, spec.ID, err)
	}
	return p, nil
}

// Types lists registered protocol type names, sorted alphabetically.
func (r *FactoryRegistry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.registered))
	copy(out, r.registered)
	sort.Strings(out)
	return out
}

// HasType reports whether typeID has a registered factory.
func (r *FactoryRegistry) HasType(typeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.factories[strings.ToLower(strings.TrimSpace(typeID))]
	return ok
}

// defaultFactories holds every transport Forcefield ships with. It is
// populated once at init and never mutated afterwards.
var defaultFactories = newDefaultFactories()

// DefaultFactories returns the built-in transport registry.
func DefaultFactories() *FactoryRegistry { return defaultFactories }

func newDefaultFactories() *FactoryRegistry {
	r := NewFactoryRegistry()
	r.MustRegister("ollama", func(spec Spec) (ModelProvider, error) {
		return NewOllamaProvider(spec.BaseURL, spec.Model), nil
	})
	r.MustRegister("openai-compatible", func(spec Spec) (ModelProvider, error) {
		return NewOpenAICompatible(spec), nil
	})
	r.MustRegister("anthropic", func(spec Spec) (ModelProvider, error) {
		return NewAnthropicProvider(spec), nil
	})
	r.MustRegister("gemini", func(spec Spec) (ModelProvider, error) {
		return NewGeminiProvider(spec), nil
	})
	r.MustRegister("vertex", func(spec Spec) (ModelProvider, error) {
		return NewVertexProvider(spec), nil
	})
	r.MustRegister("bedrock", func(spec Spec) (ModelProvider, error) {
		return NewBedrockProvider(spec), nil
	})
	r.MustRegister("azure", func(spec Spec) (ModelProvider, error) {
		return NewAzureProvider(spec), nil
	})
	r.MustRegister("openai-responses", func(spec Spec) (ModelProvider, error) {
		return NewOpenAIResponses(spec), nil
	})
	r.MustRegister("opencode-zen", func(spec Spec) (ModelProvider, error) {
		return NewOpenCodeZen(spec)
	})
	r.MustRegister("opencode-go", func(spec Spec) (ModelProvider, error) {
		return NewOpenCodeGo(spec)
	})
	return r
}
