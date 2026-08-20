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
