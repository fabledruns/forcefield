package agent

import (
	"fmt"
	"sort"
	"strings"
)

// ErrAlreadyRegistered is returned when Register is called with a duplicate name.
var ErrAlreadyRegistered = fmt.Errorf("agent already registered")

// ErrNotFound is returned when Get is called with an unknown name.
var ErrNotFound = fmt.Errorf("agent not found")

// Registry holds the set of known agent definitions. It is instance-scoped
// (owned by Runtime) and effectively immutable after construction: Register
// is only called during startup before the loop begins.
type Registry struct {
	byName map[string]Definition
	order  []string // registration order for deterministic List
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Definition)}
}

// Register adds d to the registry. Names must be unique (case-insensitive
// check, but stored as provided lowercased). d is validated.
func (r *Registry) Register(d Definition) error {
	if err := d.Validate(); err != nil {
		return err
	}
	key := strings.ToLower(strings.TrimSpace(d.Name))
	if key == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	if _, exists := r.byName[key]; exists {
		return fmt.Errorf("register agent %q: %w", d.Name, ErrAlreadyRegistered)
	}
	// Normalise name to lowercase for consistent lookups.
	d.Name = key
	r.byName[key] = d.Clone()
	r.order = append(r.order, key)
	return nil
}

// Get returns the definition for name (case-insensitive). The returned
// value is a copy.
func (r *Registry) Get(name string) (Definition, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if d, ok := r.byName[key]; ok {
		return d.Clone(), nil
	}
	return Definition{}, fmt.Errorf("unknown agent %q: %w (available: %s)", name, ErrNotFound, strings.Join(r.Names(), ", "))
}

// MustGet is like Get but panics on unknown name. For tests/builtins.
func (r *Registry) MustGet(name string) Definition {
	d, err := r.Get(name)
	if err != nil {
		panic(err)
	}
	return d
}

// List returns all definitions in registration order. Each is a copy.
func (r *Registry) List() []Definition {
	out := make([]Definition, 0, len(r.order))
	for _, k := range r.order {
		out = append(out, r.byName[k].Clone())
	}
	return out
}

// Names returns sorted agent names for error messages.
func (r *Registry) Names() []string {
	names := append([]string(nil), r.order...)
	sort.Strings(names)
	return names
}

// Update replaces an existing definition. Used only during construction
// to apply config overrides; not for use after the registry is frozen.
func (r *Registry) Update(d Definition) error {
	if err := d.Validate(); err != nil {
		return err
	}
	key := strings.ToLower(strings.TrimSpace(d.Name))
	if key == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	if _, exists := r.byName[key]; !exists {
		return fmt.Errorf("update agent %q: %w", d.Name, ErrNotFound)
	}
	d.Name = key
	r.byName[key] = d.Clone()
	return nil
}

// Default returns the general agent definition (fallback). Panics if missing.
func (r *Registry) Default() Definition {
	return r.MustGet("general")
}

// Clone returns a deep copy of the registry.
func (r *Registry) Clone() *Registry {
	out := NewRegistry()
	for _, k := range r.order {
		_ = out.Register(r.byName[k])
	}
	return out
}

// DefaultRegistry returns a registry pre-populated with the 7 built-in
// agents. Each caller gets an independent copy.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	for _, d := range builtInDefinitions() {
		if err := r.Register(d); err != nil {
			panic(fmt.Sprintf("register built-in %q: %v", d.Name, err))
		}
	}
	return r
}
