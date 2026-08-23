// Package builtin wires Forcefield's built-in tool implementations
// (filesystem, shell) into a tools.Manager.
package builtin

import (
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
	"forcefield/internal/tools/filesystem"
	"forcefield/internal/tools/shell"
)

// Option customizes how built-in tools are constructed.
type Option func(*options)

type options struct {
	executor sandbox.Executor
}

// WithExecutor routes shell commands through the given sandbox.Executor.
// Without it, the shell tool uses native execution (no isolation).
func WithExecutor(e sandbox.Executor) Option {
	return func(o *options) { o.executor = e }
}

// Register adds every built-in tool to m. Call it once, right after
// constructing the Manager and before any Execute calls.
func Register(m *tools.Manager, opts ...Option) error {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}

	all := []tools.Tool{
		filesystem.NewReadFile(),
		filesystem.NewWriteFile(),
		filesystem.NewListFiles(),
		shell.NewPWD(),
		newShell(o),
	}

	for _, t := range all {
		if err := m.Register(t); err != nil {
			return err
		}
	}

	return nil
}

// newShell picks the shell constructor matching the options.
func newShell(o options) tools.Tool {
	if o.executor != nil {
		return shell.NewShellWithExecutor(o.executor)
	}
	return shell.NewShell()
}

// NewManager returns a Manager with every built-in tool already
// registered.
func NewManager(opts ...Option) (*tools.Manager, error) {
	m := tools.NewManager(tools.NewRegistry())
	if err := Register(m, opts...); err != nil {
		return nil, err
	}
	return m, nil
}
