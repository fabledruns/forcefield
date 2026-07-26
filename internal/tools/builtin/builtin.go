// Package builtin wires Forcefield's built-in tool implementations
// (filesystem, shell) into a tools.Manager. 
package builtin

import (
	"forcefield/internal/tools"
	"forcefield/internal/tools/filesystem"
	"forcefield/internal/tools/shell"
)

// Register adds every built-in tool to m. Call it once, right after
// constructing the Manager and before any Execute calls.
func Register(m *tools.Manager) error {
	all := []tools.Tool{
		filesystem.NewReadFile(),
		filesystem.NewWriteFile(),
		filesystem.NewListFiles(),
		shell.NewPWD(),
	}

	for _, t := range all {
		if err := m.Register(t); err != nil {
			return err
		}
	}

	return nil
}

// NewManager returns a Manager with every built-in tool already registered
func NewManager() (*tools.Manager, error) {
	m := tools.NewManager(tools.NewRegistry())
	if err := Register(m); err != nil {
		return nil, err
	}
	return m, nil
}
