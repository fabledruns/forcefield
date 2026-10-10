// Package sandbox defines Forcefield's execution boundary for shell
// commands: tools request execution, executors enforce policy, and the UI
// only displays what an executor reports. See docs/Sandbox.md for modes,
// guarantees, and limits.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
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
	// ModeIsolated executes commands on Linux under a Landlock
	// filesystem ruleset plus no_new_privs (see isolated_linux.go).
	// It requires Linux with a Landlock-capable kernel; it never
	// silently falls back to native.
	ModeIsolated Mode = "isolated"
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
	case string(ModeIsolated):
		return ModeIsolated, nil
	default:
		return "", fmt.Errorf("unknown sandbox mode %q (supported: \"native\", \"wsl\", \"isolated\")", s)
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
	// Strict pins the shell working directory to Workspace (same pipeline
	// as wsl); filesystem tools are confined regardless. See docs/Sandbox.md.
	Strict bool
	// Distro selects a WSL distribution (mode wsl only). Empty means the
	// system default distribution.
	Distro string
	// Network is the requested network policy (mode wsl honors it;
	// native always has host networking).
	Network Network
	// CredentialEnv names the host environment variables Forcefield
	// itself reads as provider credentials. Native executors strip
	// these names from the inherited child environment (see
	// stripCredentialEnv); explicit per-request ExtraEnv still wins.
	// Empty preserves exact historical forwarding. Populated from
	// config.CredentialEnvNames on the runtime path.
	CredentialEnv []string
	// FSRead names extra read-only paths the isolated backend grants
	// beyond its built-in system set. Empty means the built-in set
	// only. Ignored by other backends.
	FSRead []string
	// FSWrite names extra writable paths the isolated backend grants
	// beyond the workspace and its private temp directory. Empty
	// means no extras. Ignored by other backends.
	FSWrite []string
}

// Confines reports whether filesystem paths are caged to Workspace:
// always in wsl and isolated modes, and in native mode only when
// Strict is set. Tools and executors branch on this — never on Mode
// directly — so strict-native enforces the identical invariant as the
// wsl path, and isolated always pins its shell working directory.
func (p Policy) Confines() bool {
	mode, _ := ParseMode(string(p.Mode))
	return mode == ModeWSL || mode == ModeIsolated || p.Strict
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

// envNamePattern matches the identifiers shells accept as variable
// names. It is shared (not duplicated per backend) so native, WSL
// small-path, and WSL large-path validation agree exactly.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsValidEnvName reports whether name is a valid shell environment
// variable name. Tool layers validate extra env keys through here so a
// malformed key (flag-like "-u", "A=B", empty, whitespace) fails closed
// before reaching process or sandbox setup on any backend.
func IsValidEnvName(name string) bool {
	return envNamePattern.MatchString(name)
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

// Enforcement states what a backend actually enforces. Approval UIs and
// doctor render exclusively from here. See docs/Sandbox.md.
//
// Limitations is the structured source of truth for capabilities and
// their limits (Phase 1): doctor derives warn/info verdicts from
// Limitations, never by substring-matching SummaryLines. Notes stay the
// human-readable lines rendered in SummaryLines so the approval UI and
// doctor share wording; Limitations carry the machine-stable IDs.
type Enforcement struct {
	Mode    Mode
	Distro  string // wsl mode: selected distribution, "" = default
	Network Network

	// CwdPinned: the working directory is validated to lie inside the
	// workspace before every run.
	CwdPinned bool
	// FilesystemConfined is false for shell backends except isolated
	// mode when Landlock support probes OK (WSL reaches /mnt mounts);
	// filesystem tools are confined at the tool layer.
	FilesystemConfined bool
	// NetworkEnforced: the requested Network policy is actually
	// implemented by this backend.
	NetworkEnforced bool
	// EnvForwarded: the full host environment reaches the command.
	EnvForwarded bool
	// EnvCredsStripped: the credential names in Policy.CredentialEnv
	// were removed from the forwarded environment. Zero value false
	// preserves historical wording for executors that strip nothing.
	EnvCredsStripped bool

	Notes []string

	// Limitations lists every capability limit in machine-stable form.
	// Entries with Warn=true must surface as warnings in doctor and
	// must never read as all-clear in the approval UI.
	Limitations []Limitation
}

// Limitation is one structured capability/limitation fact. ID is stable
// for tests and doctor assertions; Detail is the human wording shared
// with Notes; Warn selects the doctor verdict (true = warn, false = info).
type Limitation struct {
	ID     string
	Warn   bool
	Detail string
}

// Stable limitation IDs. Keep them stable: doctor tests and docs refer
// to these, not to English substrings.
const (
	LimFilesystemShellOpen   = "filesystem.shell-open"
	LimFilesystemToolsCaged  = "filesystem.tools-caged"
	LimFilesystemLandlock    = "filesystem.landlock"
	LimShellTextOpen         = "shell.text-open"
	LimShellTextConfined     = "shell.text-confined"
	LimShellStagedVisible    = "shell.staged-visible"
	LimNetworkInterop        = "network.wsl-interop"
	LimNetworkNamespace      = "network.namespace"
	LimEnvFullHost           = "env.full-host"
	LimEnvCredsStripped      = "env.creds-stripped"
	LimEnvRestrictedLauncher = "env.restricted-launcher"
	LimProcessUnixPgroup     = "process.unix-pgroup"
	LimProcessWindowsJob     = "process.windows-job"
	LimProcessWSLRelay       = "process.wsl-relay"
	LimPlatformWSLWindows    = "platform.wsl-windows-only"
	LimPlatformHostOnly      = "platform.host-only"
	LimMCPUnsandboxed        = "mcp.unsandboxed"
)

// Warnings returns the Warn=true subset of Limitations in order.
func (e Enforcement) Warnings() []Limitation {
	out := make([]Limitation, 0, len(e.Limitations))
	for _, l := range e.Limitations {
		if l.Warn {
			out = append(out, l)
		}
	}
	return out
}

// MCPUnsandboxedLimitation is the single source of truth for the MCP
// trust statement: MCP servers run with OS privileges, outside any
// sandbox or workspace boundary. Doctor and any future approval surface
// must render this exact Detail as a warning.
func MCPUnsandboxedLimitation() Limitation {
	return Limitation{
		ID:   LimMCPUnsandboxed,
		Warn: true,
		Detail: "MCP servers run UNSANDBOXED with your OS user privileges: no sandbox executor, " +
			"no workspace boundary, opaque arguments; cwd is the launch directory only",
	}
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
		// Qualified to Linux sockets on purpose: Windows .exe interop
		// runs on the host stack inside the same namespace (see
		// network.wsl-interop), so an unqualified "isolated" would
		// overclaim egress denial.
		netLine = "disabled - enforced for Linux sockets (isolated network namespace)"
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
	} else if e.EnvCredsStripped {
		env = "full host environment minus Forcefield credential variables"
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
	if e.Mode == ModeIsolated {
		s := "Landlock filesystem confinement"
		if !e.FilesystemConfined {
			s += " (unavailable on this kernel)"
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
	case ModeIsolated:
		return "isolated"
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
	case ModeIsolated:
		return newIsolatedExecutor(p)
	default:
		return newNativeExecutor(p)
	}
}
