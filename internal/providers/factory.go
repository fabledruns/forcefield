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
	// "anthropic", "gemini"). Service presets normalize to their
	// protocol before construction.
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
}

// Validate checks a spec's invariants before an adapter is constructed.
func (s Spec) Validate() error {
	if s.Type == "" {
		return fmt.Errorf("provider %q has no type", s.ID)
	}
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
	for name := range s.Headers {
		if !isHTTPHeaderName(name) {
			return fmt.Errorf("provider %q has an invalid header name %q", s.ID, name)
		}
	}
	return nil
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
	return r
}
