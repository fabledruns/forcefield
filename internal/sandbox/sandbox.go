// Package sandbox defines Forcefield's execution boundary for shell
// commands: an explicit policy describing what a command may do, and
// executors that enforce (or honestly decline to enforce) that policy.
//
// The architectural rule is: tools request execution, executors enforce
// policy, and the UI only displays what an executor reports about itself.
// Nothing outside this package decides how a command reaches the OS.
//
// Two modes exist:
//
//	native - exactly the historical behavior: Bash on the host (Unix) or
//	         the WSL relay used for Bash availability (Windows), with the
//	         full host environment forwarded and NO isolation of any kind.
//	wsl    - commands run inside a WSL distribution under an explicitly
//	         restricted invocation: pinned working directory, no host
//	         environment forwarding, and best-effort network isolation via
//	         an in-distribution network namespace. See the package
//	     documentation in wsl_windows.go for precisely what is and is
//	         NOT isolated; the short version is that WSL alone does not
//	         confine filesystems, and this package refuses to pretend it
//	         does.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Mode selects the execution backend for shell commands.
type Mode string

const (
	// ModeNative preserves historical behavior: no isolation. On Unix,
	// Bash runs directly on the host. On Windows, commands are relayed
	// through wsl.exe purely for Bash availability, with full host
	// access. Native is never described as sandboxed.
	ModeNative Mode = "native"
	// ModeWSL executes commands inside a WSL distribution under an
	// explicit restricted policy (see wsl_windows.go). It requires
	// Windows; it never silently falls back to native.
	ModeWSL Mode = "wsl"
)

// ParseMode converts a configuration string into a Mode. Empty means
// native, preserving behavior for users who have never configured a
// sandbox.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "", string(ModeNative):
		return ModeNative, nil
	case string(ModeWSL):
		return ModeWSL, nil
	default:
		return "", fmt.Errorf("unknown sandbox mode %q (supported: \"native\", \"wsl\")", s)
	}
}

// Network expresses the requested network policy. It is advisory for
// backends that cannot enforce it; Enforcement.NetworkEnforced carries
// the truth.
type Network string

const (
	// NetworkHost inherits host (or WSL) networking. Never isolated.
	NetworkHost Network = "host"
	// NetworkDisabled requests denial of outbound network access. In WSL
	// mode this is enforced via an in-distribution network namespace when
	// the kernel permits it, and refused otherwise (fail closed).
	NetworkDisabled Network = "disabled"
)

// ParseNetwork converts a configuration string into a Network policy.
// Empty means disabled, the safe default for sandboxed execution.
func ParseNetwork(s string) (Network, error) {
	switch s {
	case "", string(NetworkDisabled):
		return NetworkDisabled, nil
	case string(NetworkHost):
		return NetworkHost, nil
	default:
		return "", fmt.Errorf("unknown sandbox network policy %q (supported: \"disabled\", \"host\")", s)
	}
}

// Policy is the complete execution contract for a command. Tools produce
// Requests; the active executor enforces the Policy against them.
type Policy struct {
	// Mode selects the backend. Default (zero value "") means native.
	Mode Mode
	// Workspace is the absolute host path of the directory commands are
	// confined to for working-directory purposes. Empty means the
	// process's current working directory, resolved per request.
	Workspace string
	// Distro selects a WSL distribution (mode wsl only). Empty means the
	// system default distribution.
	Distro string
	// Network is the requested network policy (mode wsl honors it;
	// native always has host networking).
	Network Network
}

// DefaultPolicy returns the historical execution policy: native mode,
// host networking.
func DefaultPolicy() Policy {
	return Policy{Mode: ModeNative, Network: NetworkHost}
}

// Validate checks the policy's internal consistency so malformed values
// surface at startup instead of mid-command. A distribution name is
// constrained to a safe charset because it is placed after a flag on the
// wsl.exe command line; a hostile value must never be able to become an
// argument of its own.
func (p Policy) Validate() error {
	mode, err := ParseMode(string(p.Mode))
	if err != nil {
		return err
	}
	if _, err := ParseNetwork(string(p.Network)); err != nil {
		return err
	}
	if mode == ModeWSL && p.Distro != "" && !ValidDistroName(p.Distro) {
		return fmt.Errorf("invalid WSL distribution name %q (allowed: letters, digits, '.', '_', '-')", p.Distro)
	}
	return nil
}

// maxDistroNameLen caps distribution-name length defensively.
const maxDistroNameLen = 64

// ValidDistroName reports whether name is safe to pass as a wsl.exe flag
// VALUE: it can neither contain flag-like prefixes nor shell metacharacters,
// and it is never empty when required.
func ValidDistroName(name string) bool {
	if name == "" || len(name) > maxDistroNameLen {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			// Letters are safe anywhere.
		case r >= '0' && r <= '9':
			// Digits are safe anywhere.
		case i > 0 && (r == '_' || r == '.' || r == '-'):
			// Punctuation is safe only after the first character, so a
			// value can never start with "-" and masquerade as a flag.
		default:
			return false
		}
	}
	return true
}

// effectiveNetwork returns the policy's network request with the empty
// value resolved to its configured meaning (disabled). Comparisons
// elsewhere must use this rather than the raw field, whose zero value
// would otherwise read as "host".
func (p Policy) effectiveNetwork() Network {
	n, err := ParseNetwork(string(p.Network))
	if err != nil {
		return NetworkDisabled // validated policies never hit this
	}
	return n
}

// Request is a tool's ask to execute a command. It carries no policy:
// policy lives with the executor.
type Request struct {
	// Command is Bash command text. It is untrusted content executed by
	// Bash inside the selected backend; backends never reinterpret it on
	// the host side.
	Command string
	// Dir is the requested working directory ("" means the workspace).
	Dir string
	// ExtraEnv lists K=V pairs to add to the command's environment.
	ExtraEnv []string
}

// Prepared is a validated, ready-to-start command plus any backend
// staging cleanup. Process lifecycle (start, streaming, kill-on-cancel)
// remains the caller's; backends own only construction and their own
// staging resources.
type Prepared struct {
	Cmd *exec.Cmd
	// Dir is the resolved working directory the command will run in.
	Dir string
	// Cleanup releases backend staging resources (e.g. spilled script
	// files). Nil-safe.
	Cleanup func()
}

// Sentinel errors executors return; callers match with errors.Is to turn
// them into actionable tool results.
var (
	// ErrWorkspaceEscape means the requested directory resolves outside
	// the policy's workspace. Scope is never silently expanded.
	ErrWorkspaceEscape = errors.New("path escapes the sandboxed workspace")
	// ErrInvalidDir means the requested working directory does not exist
	// or is unusable.
	ErrInvalidDir = errors.New("working directory does not exist")
	// ErrBackendUnavailable means the selected backend cannot run
	// commands right now (WSL missing, broken distribution, network
	// isolation impossible). Executors never fall back to another
	// backend.
	ErrBackendUnavailable = errors.New("execution backend unavailable")
	// ErrUnsupported means the requested mode cannot exist on this
	// platform at all.
	ErrUnsupported = errors.New("execution mode unsupported on this platform")
)

// Enforcement states what a backend actually enforces for a given
// policy. Approval UIs and doctor derive their wording exclusively from
// this struct so the interface can never claim more than the executor
// delivers. Booleans are used over free-form strings wherever a fact is
// binary, so rendering stays consistent; Notes carry the honest fine
// print.
type Enforcement struct {
	Mode    Mode
	Distro  string // wsl mode: selected distribution, "" = default
	Network Network

	// CwdPinned: the working directory is validated to lie inside the
	// workspace before every run.
	CwdPinned bool
	// FilesystemConfined: general filesystem access beyond the working
	// directory is prevented. For shell, NO BACKEND SETS THIS TRUE - WSL
	// distributions reach all Windows drives through /mnt automounts.
	// Filesystem tools (read_file, write_file, list_files) ARE confined
	// to the workspace when Mode is wsl via tool-layer policy (see
	// internal/tools/filesystem); shell remains not confined.
	FilesystemConfined bool
	// NetworkEnforced: the requested Network policy is actually
	// implemented by this backend.
	NetworkEnforced bool
	// EnvForwarded: the full host environment reaches the command.
	EnvForwarded bool

	Notes []string
}

// SummaryLines renders the enforcement facts as stable human-readable
// lines ("Label: value"). This is the single source of wording for the
// approval modal, ff doctor, and tests, so the UI cannot drift from what
// the executor actually does.
func (e Enforcement) SummaryLines() []string {
	const w = 12 // width of the widest label, "Environment:"
	pad := func(label, value string) string {
		return label + ":" + strings.Repeat(" ", 1+w-len(label)) + value
	}

	// Derive the env fact from the mode so a partially filled struct can
	// never mislabel native as restricted (native forwards by definition).
	envForwarded := e.EnvForwarded || e.Mode == ModeNative

	// Normalize the network request the same way Policy does so a zero
	// value reads as its configured meaning (disabled), never as host.
	net, err := ParseNetwork(string(e.Network))
	if err != nil {
		net = NetworkDisabled
	}

	lines := []string{}

	execution := e.Mode.DisplayName()
	switch {
	case e.Distro != "":
		execution += fmt.Sprintf(" (%s)", e.Distro)
	case e.Mode == ModeWSL:
		execution += " (default distribution)"
	}
	lines = append(lines, pad("Execution", execution))

	fs := "host user permissions"
	if e.CwdPinned {
		fs = "working directory pinned to the project workspace"
	}
	if e.FilesystemConfined {
		fs = "confined to the project workspace"
	} else if e.CwdPinned {
		fs += " (other paths are NOT blocked)"
	}
	lines = append(lines, pad("Filesystem", fs))

	netLine := "host network"
	if net == NetworkDisabled {
		netLine = "disabled - enforced (isolated network namespace)"
		if !e.NetworkEnforced {
			netLine = "disabled - NOT enforced; the command may use host networking"
		}
	} else if e.Mode == ModeWSL {
		netLine = "inherits WSL/host networking (not isolated)"
	}
	lines = append(lines, pad("Network", netLine))

	env := "full host environment"
	if !envForwarded {
		env = "restricted (host variables are not forwarded)"
	}
	lines = append(lines, pad("Environment", env))
	lines = append(lines, pad("Isolation", e.isolation()))

	for _, note := range e.Notes {
		lines = append(lines, pad("Note", note))
	}
	return lines
}

// isolation summarizes the overall boundary in one phrase.
func (e Enforcement) isolation() string {
	if e.Mode == ModeWSL {
		s := "WSL execution boundary"
		net, _ := ParseNetwork(string(e.Network))
		if !e.NetworkEnforced && net == NetworkDisabled {
			s += " (network isolation unavailable)"
		}
		return s
	}
	return "none"
}

// DisplayName returns the human-facing mode name.
func (m Mode) DisplayName() string {
	switch m {
	case ModeWSL:
		return "WSL"
	default:
		return "native"
	}
}

// Executor enforces a Policy for shell command execution. Implementations
// must be safe for concurrent use.
type Executor interface {
	// Prepare validates req against the policy and returns a
	// ready-to-start command. It never starts the process and never falls
	// back to a different backend.
	Prepare(ctx context.Context, req Request) (*Prepared, error)
	// Probe verifies the backend is usable right now (interpreter
	// present, WSL healthy, requested isolation mechanisms available).
	Probe(ctx context.Context) error
	// Describe reports what this executor enforces. It must reflect
	// reality, including limitations, because it feeds approval UIs.
	Describe(ctx context.Context) Enforcement
}

// NewExecutor constructs the executor for p's mode. It fails rather than
// substituting a different backend: a policy that cannot be honored is an
// error, never a silent downgrade.
func NewExecutor(p Policy) (Executor, error) {
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid sandbox policy: %w", err)
	}

	mode, _ := ParseMode(string(p.Mode))
	switch mode {
	case ModeWSL:
		return newWSLExecutor(p)
	default:
		return newNativeExecutor(p)
	}
}
