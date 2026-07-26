package tools

import (
	"context"
	"fmt"
)

// Manager is the entry point the rest of Forcefield uses to work with tools
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

// Execute looks up name and runs it with args. A nil args is treated as
// empty, so tools that take no arguments (e.g. pwd) can be called
// without callers special-casing them.
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
