package shell

import (
	"context"
	"sync"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// Shell executes commands with piped, sanitized output and live streaming.
// Terminal ownership stays with the TUI; construction stays with the
// sandbox executor. See docs/Tools.md.
type Shell struct {
	// executor builds validated commands. Nil means native mode with the
	// historical unrestricted behavior; runtime.New wires the configured
	// sandbox executor in.
	executor sandbox.Executor
	// executorOnce guards one-time construction of the default executor.
	executorOnce sync.Once

	// backendMu guards the one-time backend probe below.
	backendMu sync.Mutex
	// backendErr caches the result of probing the Bash execution backend
	// (WSL availability on Windows; see ensureBackend). A nil backendErr
	// with backendProbed set means the backend is healthy.
	backendErr    error
	backendProbed bool

	// limits overrides the default output/timeout bounds. Zero values
	// resolve via tools.DefaultLimitsFor("shell"); apply configured
	// overrides with SetLimits before registering.
	limits tools.Limits
}

// SetLimits overrides the shell output/timeout bounds. Only positive
// fields take effect; the rest resolve to the tool defaults.
func (s *Shell) SetLimits(l tools.Limits) {
	s.limits = l
}

// ToolLimits reports the resolved bounds, letting the scheduler apply
// the configured timeout end to end.
func (s *Shell) ToolLimits() tools.Limits {
	return s.resolveLimits()
}

func (s *Shell) resolveLimits() tools.Limits {
	if s == nil {
		return tools.DefaultLimitsFor("shell")
	}
	return s.limits.WithDefaults(tools.DefaultLimitsFor("shell"))
}

// NewShell returns a ready-to-register Shell tool using native execution.
func NewShell() *Shell { return &Shell{} }

// NewShellWithExecutor returns a Shell tool whose commands are built by
// the given sandbox.Executor. The executor is authoritative: when it
// refuses (unavailable WSL, workspace escape, impossible network
// isolation), the tool reports an error instead of falling back.
func NewShellWithExecutor(e sandbox.Executor) *Shell { return &Shell{executor: e} }

// executorFor lazily supplies the default executor so zero-value Shells
// keep working for tests and simple callers. It is safe for concurrent use.
func (s *Shell) executorFor() sandbox.Executor {
	s.executorOnce.Do(func() {
		if s.executor != nil {
			return
		}
		e, err := sandbox.NewExecutor(sandbox.DefaultPolicy())
		if err != nil {
			panic("shell: default policy must always construct: " + err.Error())
		}
		s.executor = e
	})
	return s.executor
}

// ExecutionEnforcement lets permission surfaces describe exactly what will
// happen if this tool runs. The bool is false for tools without a
// meaningful enforcement story (everything but shell today).
func (s *Shell) ExecutionEnforcement(ctx context.Context) (sandbox.Enforcement, bool) {
	return s.executorFor().Describe(ctx), true
}

// ensureBackend probes the active executor once per Shell, caching the
// outcome so an unusable backend yields one clear structured error per
// command instead of a confusing per-command failure. A probe aborted by
// caller cancellation is reported but not cached.
func (s *Shell) ensureBackend(ctx context.Context) error {
	s.backendMu.Lock()
	defer s.backendMu.Unlock()
	if s.backendProbed {
		return s.backendErr
	}
	err := s.executorFor().Probe(ctx)
	if ctx.Err() == nil {
		s.backendProbed, s.backendErr = true, err
	}
	return err
}

// CheckBackend verifies that the Bash execution backend is usable (WSL
// availability on Windows; always healthy on Unix) and returns a
// diagnostic error when it isn't. It exists so diagnostics like
// `ff doctor` can report shell problems without executing a command.
func (s *Shell) CheckBackend(ctx context.Context) error {
	return s.ensureBackend(ctx)
}

func (*Shell) Name() string { return "shell" }

func (*Shell) Description() string {
	return "Execute a shell command and return its stdout, stderr, and exit code. " +
		"Commands are executed using GNU Bash. " +
		"Runs in the current project directory unless a working directory is given. " +
		"Commands that require an interactive terminal (editors, pagers, ssh, REPLs, etc.) are not supported."
}

func (*Shell) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute.",
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory to run the command in. Defaults to the current directory.",
			},
			"env": map[string]any{
				"type":        "object",
				"description": "Additional environment variables to set for the command, as key/value string pairs.",
			},
			"timeout_seconds": map[string]any{
				"type":        "number",
				"description": "Maximum number of seconds to allow the command to run before it is killed. Defaults to 30.",
			},
		},
		"required": []string{"command"},
	}
}

// Metadata advertises shell's execution characteristics to the scheduler
// and the model. Shell always requires explicit approval to run.
func (s *Shell) Metadata() tools.Metadata {
	return tools.Metadata{
		Timeout:              s.resolveLimits().Timeout,
		SupportsStreaming:    true,
		SupportsCancellation: true,
		SupportsParallel:     true,
		RequiredPermissions:  []tools.Permission{tools.PermissionExecuteShell},
		Retryable:            false, // shell commands are not idempotent in general
	}
}
