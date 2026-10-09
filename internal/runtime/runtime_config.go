package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forcefield/internal/config"
	"forcefield/internal/memory"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// refreshAuthState re-reads the active provider's authentication
// requirements after construction, provider switch, or model switch.
func (r *Runtime) refreshAuthState() {
	if r == nil {
		return
	}
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()
	if cfg == nil {
		r.mu.Lock()
		r.authRequired, r.authEnvVar = false, ""
		r.mu.Unlock()
		return
	}
	resolved, err := cfg.ResolveProvider(cfg.Model.Provider, cfg.Model.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.authRequired, r.authEnvVar = false, ""
		return
	}
	r.authRequired = resolved.AuthRequired
	r.authEnvVar = resolved.AuthEnvVar
}

// authSnapshot copies the auth state plus provider display name under RLock
// so background model turns never read live switchable fields.
func (r *Runtime) authSnapshot() (required bool, envVar, provider string) {
	if r == nil {
		return false, "", ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider = ""
	if r.cfg != nil {
		provider = r.cfg.Model.Provider
	}
	return r.authRequired, r.authEnvVar, provider
}

// checkAuth reports an actionable error when the active provider needs an
// API key that was not found.
func (r *Runtime) checkAuth() error {
	required, envVar, provider := r.authSnapshot()
	if !required || envVar == "" {
		return nil
	}
	if key, _, err := config.ResolveEnvValue(envVar); err == nil && key != "" {
		return nil
	}
	return fmt.Errorf(
		"%s requires an API key - set %s in your environment or .env file and restart Forcefield",
		providers.DisplayName(provider), envVar,
	)
}

// checkAuthWithSnapshot is checkAuth against an explicit snapshot so a run
// uses one consistent view even if a switch lands mid-run.
func checkAuthWithSnapshot(required bool, envVar, provider string) error {
	if !required || envVar == "" {
		return nil
	}
	if key, _, err := config.ResolveEnvValue(envVar); err == nil && key != "" {
		return nil
	}
	return fmt.Errorf(
		"%s requires an API key - set %s in your environment or .env file and restart Forcefield",
		providers.DisplayName(provider), envVar,
	)
}

// limitsFromConfig builds Limits from cfg.Agent, falling back to
// DefaultLimits for any dimension left at its zero value.
func limitsFromConfig(cfg *config.Config) Limits {
	limits := DefaultLimits
	if cfg.Agent.MaxIterations > 0 {
		limits.MaxIterations = cfg.Agent.MaxIterations
	}
	if cfg.Agent.MaxToolCalls > 0 {
		limits.MaxToolCalls = cfg.Agent.MaxToolCalls
	}
	if cfg.Agent.MaxConsecutiveFailures > 0 {
		limits.MaxConsecutiveFailures = cfg.Agent.MaxConsecutiveFailures
	}
	return limits
}

// toolLimitsFromConfig converts the optional tools: overrides into the
// shared limits form. Absent entries resolve to each tool's default at
// use time, so existing configs behave exactly as before.
func toolLimitsFromConfig(cfg *config.Config) map[string]tools.Limits {
	if cfg == nil || len(cfg.Tools) == 0 {
		return nil
	}
	out := make(map[string]tools.Limits, len(cfg.Tools))
	for name, lim := range cfg.Tools {
		out[name] = tools.Limits{
			MaxBytes: lim.MaxBytes,
			MaxLines: lim.MaxLines,
			Timeout:  time.Duration(lim.TimeoutSeconds * float64(time.Second)),
		}
	}
	return out
}

// limitsForAgent overlays one agent profile's run bounds onto the
// global base: positive profile values win, everything else keeps the
// base. A nil config or unknown agent leaves the base untouched, so
// existing behavior is preserved unless a profile opts in.
func limitsForAgent(cfg *config.Config, agentName string, base Limits) Limits {
	if cfg == nil {
		return base
	}
	o, ok := cfg.Agents[agentName]
	if !ok {
		return base
	}
	out := base
	if o.MaxIterations > 0 {
		out.MaxIterations = o.MaxIterations
	}
	if o.MaxToolCalls > 0 {
		out.MaxToolCalls = o.MaxToolCalls
	}
	if o.MaxConsecutiveFailures > 0 {
		out.MaxConsecutiveFailures = o.MaxConsecutiveFailures
	}
	return out
}

// contextBudgetFromConfig builds the per-turn context budget for one
// agent profile: profile settings win, then the global agent.* block,
// then negotiated provider capabilities, then the static model table,
// then conservative defaults. Non-positive values fall through each
// layer, so existing configs keep working unchanged.
func contextBudgetFromConfig(cfg *config.Config, modelName, agentName string, caps providers.Capabilities) ContextBudget {
	limit, reserve, maxMsg := cfg.Agent.ContextWindow, cfg.Agent.ContextReserve, cfg.Agent.MaxContextMessages
	summarize := cfg.Agent.ContextSummary
	if o, ok := cfg.Agents[agentName]; ok {
		if o.ContextWindow > 0 {
			limit = o.ContextWindow
		}
		if o.ContextReserve > 0 {
			reserve = o.ContextReserve
		}
		if o.MaxContextMessages > 0 {
			maxMsg = o.MaxContextMessages
		}
		if o.ContextSummary != nil {
			summarize = *o.ContextSummary
		}
	}
	return BudgetForCaps(modelName, limit, reserve, maxMsg, summarize, caps)
}

// SetPermissionAsker replaces how "ask" permission decisions are
// resolved. The default (set in New) prompts on stdin/stdout, which
// isn't usable once something like the TUI has taken over the terminal;
// callers with their own interactive surface should call this before the
// first StreamChat/Run.
func (r *Runtime) SetPermissionAsker(asker permissions.Asker) {
	if r == nil || r.scheduler == nil {
		return
	}
	r.scheduler.setAsker(asker)
}

// Permissions returns the runtime's permission manager, e.g. for a
// "/permissions" command that lists or edits the current rules.
func (r *Runtime) Permissions() *permissions.Manager {
	if r == nil || r.scheduler == nil {
		return nil
	}
	return r.scheduler.permissions
}

// WorkspaceRoot returns the resolved project root filesystem tools and
// the shell executor are scoped to in confining modes.
func (r *Runtime) WorkspaceRoot() string {
	if r == nil {
		return ""
	}
	return r.workspaceRoot
}

// WorkspaceCwd returns the process working directory captured at
// construction: the permissive-mode anchor.
func (r *Runtime) WorkspaceCwd() string {
	if r == nil {
		return ""
	}
	return r.workspaceCwd
}

// newPolicyWithRoot builds the sandbox policy for an already-resolved
// project root, so callers that computed the root once (one git
// invocation) do not pay for a second lookup. Behavior mirrors
// ResolveWorkspace exactly: an explicit workspace.root wins, otherwise
// the shared root, falling back to cwd when its resolution failed.
func newPolicyWithRoot(cfg *config.Config, cwd, sharedRoot string, rootErr error) (sandbox.Policy, error) {
	mode, err := sandbox.ParseMode(cfg.Sandbox.Mode)
	if err != nil {
		return sandbox.Policy{}, fmt.Errorf("invalid sandbox.mode: %w", err)
	}
	network, err := sandbox.ParseNetwork(cfg.Sandbox.WSL.Network)
	if err != nil {
		return sandbox.Policy{}, fmt.Errorf("invalid sandbox.wsl.network: %w", err)
	}
	root, err := resolveWorkspaceRoot(cfg, cwd, sharedRoot, rootErr)
	if err != nil {
		return sandbox.Policy{}, err
	}
	return sandbox.Policy{
		Mode:      mode,
		Workspace: root,
		Strict:    cfg.Workspace.Mode == config.WorkspaceStrict,
		Distro:    cfg.Sandbox.WSL.Distribution,
		Network:   network,
		// Native shell/job children must not inherit the credential
		// variables Forcefield itself reads; explicit per-command env
		// still applies. See sandbox.stripCredentialEnv.
		CredentialEnv: config.CredentialEnvNames(cfg),
	}, nil
}

// resolveWorkspaceRoot mirrors ResolveWorkspace for a pre-resolved root:
// explicit workspace.root wins (absolute, or relative to the startup
// directory, and it must exist); otherwise the shared root, or cwd when
// resolving it failed (ResolveWorkspace swallows that failure the same
// way — the fallback chain never errors on missing git).
func resolveWorkspaceRoot(cfg *config.Config, cwd, sharedRoot string, rootErr error) (string, error) {
	if cfg != nil && strings.TrimSpace(cfg.Workspace.Root) != "" {
		return resolveExplicitRoot(cfg.Workspace.Root)
	}
	if rootErr != nil {
		return cwd, nil
	}
	return sharedRoot, nil
}

// newPolicy builds the sandbox policy for the resolved workspace root.
// The root and strict flag come from ResolveWorkspace/workspace.mode, so
// filesystem tools and the shell executor share one boundary.
func newPolicy(cfg *config.Config) (sandbox.Policy, error) {
	mode, err := sandbox.ParseMode(cfg.Sandbox.Mode)
	if err != nil {
		return sandbox.Policy{}, fmt.Errorf("invalid sandbox.mode: %w", err)
	}
	network, err := sandbox.ParseNetwork(cfg.Sandbox.WSL.Network)
	if err != nil {
		return sandbox.Policy{}, fmt.Errorf("invalid sandbox.wsl.network: %w", err)
	}
	root, err := ResolveWorkspace(cfg)
	if err != nil {
		return sandbox.Policy{}, err
	}
	return sandbox.Policy{
		Mode:      mode,
		Workspace: root,
		Strict:    cfg.Workspace.Mode == config.WorkspaceStrict,
		Distro:    cfg.Sandbox.WSL.Distribution,
		Network:   network,
		// Same credential stripping as newPolicyWithRoot above.
		CredentialEnv: config.CredentialEnvNames(cfg),
	}, nil
}

// newExecutor constructs the sandbox executor for shell commands from the
// configured sandbox section and the current project root. Construction
// fails loudly for unsupported combinations (e.g. wsl mode on Unix);
// there is never a silent fallback to a weaker backend.
func newExecutor(cfg *config.Config) (sandbox.Executor, error) {
	policy, err := newPolicy(cfg)
	if err != nil {
		return nil, err
	}
	executor, err := sandbox.NewExecutor(policy)
	if err != nil {
		return nil, fmt.Errorf("create %s executor (sandbox.mode = %q): %w", policy.Mode, cfg.Sandbox.Mode, err)
	}
	return executor, nil
}

// ResolveWorkspace resolves the effective root (explicit root, else Git
// top-level, else cwd). Single resolution point; see docs/Runtime.md.
func ResolveWorkspace(cfg *config.Config) (string, error) {
	if cfg != nil && strings.TrimSpace(cfg.Workspace.Root) != "" {
		return resolveExplicitRoot(cfg.Workspace.Root)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	root, err := memory.ProjectRoot(cwd)
	if err != nil {
		return cwd, nil
	}
	return root, nil
}

// resolveExplicitRoot absolutizes a configured root (relative roots
// anchor at the startup directory) and requires it to exist.
func resolveExplicitRoot(root string) (string, error) {
	orig := root
	if !filepath.IsAbs(root) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve workspace.root %q: %w", orig, err)
		}
		root = filepath.Join(cwd, root)
	}
	abs := filepath.Clean(root)
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("workspace.root %q: %w", orig, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace.root %q is not a directory", orig)
	}
	// Canonicalize symlinks/junctions once so containment checks and
	// doctor reporting use one stable spelling.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}
