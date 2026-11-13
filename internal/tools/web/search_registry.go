package web

import (
	"fmt"
	"sync"
)

// Registry holds registered SearchProviders and manages default/fallback selection.
type Registry struct {
	mu           sync.RWMutex
	providers    map[string]SearchProvider
	order        []string // registration order
	defaultName  string
	fallbackName string
}

// SearchRegistry is an alias for Registry.
type SearchRegistry = Registry

// NewSearchRegistry is an alias for NewRegistry.
var NewSearchRegistry = NewRegistry

// NewRegistry returns an empty SearchProvider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]SearchProvider),
	}
}

// Register adds a provider to the registry.
// The first provider registered automatically becomes the default.
// Returns an error on empty name or duplicate registration.
func (r *Registry) Register(p SearchProvider) error {
	if p == nil {
		return fmt.Errorf("register: nil provider")
	}
	name := p.Name()
	if name == "" {
		return fmt.Errorf("register: provider has empty name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("register: provider %q already registered", name)
	}

	r.providers[name] = p
	r.order = append(r.order, name)

	// First provider becomes the default automatically.
	if r.defaultName == "" {
		r.defaultName = name
	}

	return nil
}

// Lookup returns the named provider, or an error if not found.
func (r *Registry) Lookup(name string) (SearchProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrBackendNotFound, name)
	}
	return p, nil
}

// SetDefault sets the named provider as the default.
func (r *Registry) SetDefault(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.providers[name]; !ok {
		return fmt.Errorf("%w: cannot set default to unknown provider %q", ErrBackendNotFound, name)
	}
	r.defaultName = name
	return nil
}

// SetFallback sets the named provider as the fallback for when the default fails.
func (r *Registry) SetFallback(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.providers[name]; !ok {
		return fmt.Errorf("%w: cannot set fallback to unknown provider %q", ErrBackendNotFound, name)
	}
	r.fallbackName = name
	return nil
}

// Default returns the current default provider.
func (r *Registry) Default() (SearchProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.defaultName == "" {
		return nil, fmt.Errorf("%w: no providers registered", ErrBackendNotFound)
	}
	return r.providers[r.defaultName], nil
}

// Fallback returns the fallback provider, or nil if none is configured.
func (r *Registry) Fallback() SearchProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.fallbackName == "" {
		return nil
	}
	return r.providers[r.fallbackName]
}

// All returns all registered providers in registration order.
func (r *Registry) All() []SearchProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]SearchProvider, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.providers[name])
	}
	return out
}

// Names returns registered provider names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, len(r.order))
	copy(names, r.order)
	return names
}

// Resolve returns the best available provider: preference first if set,
// then default, then first registered. Returns an error if no providers
// are registered.
func (r *Registry) Resolve(preference string) (SearchProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if preference != "" {
		if p, ok := r.providers[preference]; ok {
			return p, nil
		}
	}

	if r.defaultName != "" {
		return r.providers[r.defaultName], nil
	}

	if len(r.order) > 0 {
		return r.providers[r.order[0]], nil
	}

	return nil, fmt.Errorf("%w: no providers registered", ErrBackendNotFound)
}
