// Package builtin wires Forcefield's built-in tool implementations
// (filesystem, shell) into a tools.Manager.
package builtin

import (
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
	"forcefield/internal/tools/filesystem"
	"forcefield/internal/tools/git"
	"forcefield/internal/tools/search"
	"forcefield/internal/tools/security"
	"forcefield/internal/tools/shell"
)

// Option customizes how built-in tools are constructed.
type Option func(*options)

type options struct {
	executor sandbox.Executor
	policy   sandbox.Policy
	limits   map[string]tools.Limits
}

// WithExecutor routes shell commands through the given sandbox.Executor.
// Without it, the shell tool uses native execution (no isolation).
func WithExecutor(e sandbox.Executor) Option {
	return func(o *options) { o.executor = e }
}

// WithPolicy selects the workspace root filesystem tools
// (read_file, write_file, list_files, search_files, find_files,
// search_code, git, secret_scan) are confined to. Confinement itself is
// unconditional; the policy only selects the root (an empty Workspace
// cages to the process working directory at call time). The shell
// executor is configured separately via WithExecutor and keeps its own
// documented behavior.
func WithPolicy(p sandbox.Policy) Option {
	return func(o *options) { o.policy = p }
}

// WithLimits applies per-tool output/timeout overrides. Keys are tool
// names; tools that do not implement tools.LimitsSetter ignore their
// entry. Only positive fields take effect per tool.
func WithLimits(m map[string]tools.Limits) Option {
	return func(o *options) { o.limits = m }
}

// applyLimits hands a tool its configured override when present.
func applyLimits(t tools.Tool, m map[string]tools.Limits) {
	if len(m) == 0 {
		return
	}
	l, ok := m[t.Name()]
	if !ok {
		return
	}
	if s, ok := t.(tools.LimitsSetter); ok {
		s.SetLimits(l)
	}
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
		newShellJob(o),
		newSearchFiles(o),
		newFindFiles(o),
		newSearchCode(o),
		newGit(o),
		newSecretScan(o),
	}

	for _, t := range all {
		applyLimits(t, o.limits)
		if err := m.Register(t); err != nil {
			return err
		}
	}

	return nil
}

func newReadFile(o options) tools.Tool {
	return filesystem.NewReadFileWithPolicy(o.policy)
}

func newWriteFile(o options) tools.Tool {
	return filesystem.NewWriteFileWithPolicy(o.policy)
}

func newListFiles(o options) tools.Tool {
	return filesystem.NewListFilesWithPolicy(o.policy)
}

// newShell picks the shell constructor matching the options.
func newShell(o options) tools.Tool {
	if o.executor != nil {
		return shell.NewShellWithExecutor(o.executor)
	}
	return shell.NewShell()
}

// newShellJob picks the background-job constructor matching the
// options, sharing the shell executor (and its workspace policy).
func newShellJob(o options) tools.Tool {
	if o.executor != nil {
		return shell.NewShellJobWithExecutor(o.executor)
	}
	return shell.NewShellJob()
}

func newSearchFiles(o options) tools.Tool {
	return search.NewSearchFilesWithPolicy(o.policy)
}

func newFindFiles(o options) tools.Tool {
	return search.NewFindFilesWithPolicy(o.policy)
}

func newSearchCode(o options) tools.Tool {
	return search.NewSearchCodeWithPolicy(o.policy)
}

func newGit(o options) tools.Tool {
	return git.NewGitWithPolicy(o.policy)
}

func newSecretScan(o options) tools.Tool {
	return security.NewSecretScanWithPolicy(o.policy)
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
