package tools

import (
	"context"
	"fmt"
)

// Manager is the entry point for tool registration and execution.
type Manager struct {
	registry *Registry
}

// NewManager returns a Manager backed by registry.
func NewManager(registry *Registry) *Manager {
	return &Manager{registry: registry}
}

// Register adds t to the underlying registry. See Registry.Register.
func (m *Manager) Register(t Tool) error {
	return m.registry.Register(t)
}

// List returns every registered tool, in registration order.
func (m *Manager) List() []Tool {
	return m.registry.All()
}

// Definitions returns the static Definition of every registered tool.
func (m *Manager) Definitions() []Definition {
	return m.registry.Definitions()
}

// Lookup returns the registered tool named name, if any.
func (m *Manager) Lookup(name string) (Tool, bool) {
	return m.registry.Lookup(name)
}

// Filtered returns a new Manager that exposes only the tools whose names
// are in allowed. Tools are reused (same instances), so executor/policy
// wiring is identical to the source manager. Unknown names are rejected.
func (m *Manager) Filtered(allowed []string) (*Manager, error) {
	if m == nil || m.registry == nil {
		return nil, fmt.Errorf("filtered manager: source manager is nil")
	}
	if allowed == nil {
		return nil, fmt.Errorf("filtered manager: allowed list cannot be nil (use empty slice for no tools)")
	}
	keep := make(map[string]struct{}, len(allowed))
	for _, n := range allowed {
		if n == "" {
			return nil, fmt.Errorf("filtered manager: tool name cannot be empty")
		}
		if _, dup := keep[n]; dup {
			return nil, fmt.Errorf("filtered manager: duplicate tool %q", n)
		}
		keep[n] = struct{}{}
	}
	// Validate all allowed names exist in source.
	for n := range keep {
		if _, ok := m.registry.Lookup(n); !ok {
			return nil, fmt.Errorf("filtered manager: unknown tool %q", n)
		}
	}
	reg := NewRegistry()
	// Preserve source registration order.
	for _, t := range m.registry.All() {
		if _, ok := keep[t.Name()]; ok {
			if err := reg.Register(t); err != nil {
				return nil, err
			}
		}
	}
	return NewManager(reg), nil
}

// Execute looks up and runs a tool. Nil args are treated as empty.
func (m *Manager) Execute(ctx context.Context, name string, args map[string]any) (Result, error) {
	tool, ok := m.registry.Lookup(name)
	if !ok {
		return Result{}, fmt.Errorf("execute %q: %w", name, ErrNotFound)
	}

	if args == nil {
		args = map[string]any{}
	}

	result, err := tool.Execute(ctx, args)
	if err != nil {
		return Result{}, &ExecutionError{Tool: name, Err: err}
	}

	return result, nil
}
