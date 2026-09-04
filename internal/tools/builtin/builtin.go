// Package builtin wires Forcefield's built-in tool implementations
// (filesystem, shell) into a tools.Manager.
package builtin

import (
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
	"forcefield/internal/tools/filesystem"
	"forcefield/internal/tools/search"
	"forcefield/internal/tools/security"
	"forcefield/internal/tools/shell"
)

// Option customizes how built-in tools are constructed.
type Option func(*options)

type options struct {
	executor  sandbox.Executor
	policy    sandbox.Policy
	hasPolicy bool
}

// WithExecutor routes shell commands through the given sandbox.Executor.
// Without it, the shell tool uses native execution (no isolation).
func WithExecutor(e sandbox.Executor) Option {
	return func(o *options) { o.executor = e }
}

// WithPolicy configures filesystem tools (read_file, write_file, list_files)
// to enforce workspace confinement when policy.Mode is wsl. In native mode
// or when no policy is supplied, filesystem tools preserve historical
// unrestricted behavior.
func WithPolicy(p sandbox.Policy) Option {
	return func(o *options) { o.policy = p; o.hasPolicy = true }
}

// WithFS remains an alias for WithPolicy for clarity at call sites that
// only care about filesystem confinement.
func WithFS(p sandbox.Policy) Option {
	return WithPolicy(p)
}

// Register adds every built-in tool to m. Call it once, right after
// constructing the Manager and before any Execute calls.
func Register(m *tools.Manager, opts ...Option) error {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}

	all := []tools.Tool{
		newReadFile(o),
		newWriteFile(o),
		newListFiles(o),
		shell.NewPWD(),
		newShell(o),
		newSearchFiles(o),
		newSecretScan(o),
	}

	for _, t := range all {
		if err := m.Register(t); err != nil {
			return err
		}
	}

	return nil
}

func newReadFile(o options) tools.Tool {
	if o.hasPolicy && o.policy.Mode == sandbox.ModeWSL {
		return filesystem.NewReadFileWithPolicy(o.policy)
	}
	// Native or unspecified: preserve historical unrestricted behavior.
	return filesystem.NewReadFile()
}

func newWriteFile(o options) tools.Tool {
	if o.hasPolicy && o.policy.Mode == sandbox.ModeWSL {
		return filesystem.NewWriteFileWithPolicy(o.policy)
	}
	return filesystem.NewWriteFile()
}

func newListFiles(o options) tools.Tool {
	if o.hasPolicy && o.policy.Mode == sandbox.ModeWSL {
		return filesystem.NewListFilesWithPolicy(o.policy)
	}
	return filesystem.NewListFiles()
}

// newShell picks the shell constructor matching the options.
func newShell(o options) tools.Tool {
	if o.executor != nil {
		return shell.NewShellWithExecutor(o.executor)
	}
	return shell.NewShell()
}

func newSearchFiles(o options) tools.Tool {
	if o.hasPolicy && o.policy.Mode == sandbox.ModeWSL {
		return search.NewSearchFilesWithPolicy(o.policy)
	}
	return search.NewSearchFiles()
}

func newSecretScan(o options) tools.Tool {
	if o.hasPolicy && o.policy.Mode == sandbox.ModeWSL {
		return security.NewSecretScanWithPolicy(o.policy)
	}
	return security.NewSecretScan()
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
