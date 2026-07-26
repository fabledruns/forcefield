package tools

import "fmt"

// Registry stores and looks up registered tools.
type Registry struct {
	byName map[string]Tool
	all    []Tool
}

// NewRegistry returns an empty Registry ready for Register calls.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Tool)}
}

// Register adds t under t.Name().
func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if name == "" {
		return fmt.Errorf("tools: cannot register a tool with an empty name")
	}

	if _, exists := r.byName[name]; exists {
		return fmt.Errorf("tools: register %q: %w", name, ErrAlreadyRegistered)
	}

	r.byName[name] = t
	r.all = append(r.all, t)
	return nil
}

func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

func (r *Registry) All() []Tool {
	return r.all
}

// Definitions returns the static Definition of every registered tool, in registration order.
func (r *Registry) Definitions() []Definition {
	defs := make([]Definition, len(r.all))
	for i, t := range r.all {
		defs[i] = Definition{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		}
	}
	return defs
}
