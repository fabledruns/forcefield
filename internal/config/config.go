// Package config loads Forcefield's YAML configuration file.
//
// Config lives at ~/.forcefield/config.yaml. If it doesn't exist, Load
// writes a sensible default file and directory structure so a first-time
// user always has something working to edit.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"forcefield/internal/mcp"
	"forcefield/internal/providers"
	"forcefield/internal/redact"
	"forcefield/internal/sandbox"
)

// Model describes the active model selection. Provider and Endpoint are
// legacy first-class fields kept for compatibility; richer per-provider
// settings live in Providers.
type Model struct {
	Provider string `yaml:"provider"`
	Endpoint string `yaml:"endpoint"`
	Name     string `yaml:"name"`
	APIKey   string `yaml:"-"`
}

// ProviderConfig is one entry under "providers:". Empty fields fall back to
// service defaults. Secrets never live here (see docs/Config.md).
type ProviderConfig struct {
	// Type selects the wire protocol or a known service preset:
	// "ollama", "openai-compatible", "anthropic", "gemini", or a service
	// id like "openai", "xai", "nvidia", "lmstudio", "llama-cpp".
	Type string `yaml:"type,omitempty"`
	// BaseURL overrides the service's default API root.
	BaseURL string `yaml:"base_url,omitempty"`
	// APIKeyEnv names the environment variable (or .env key) holding the
	// API key. Empty means "use the service's default variable".
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
	// Model optionally records the default model to switch to when this
	// provider is selected.
	Model string `yaml:"model,omitempty"`
	// Headers are extra HTTP headers sent with every request to this
	// provider (e.g. OpenRouter's HTTP-Referer).
	Headers map[string]string `yaml:"headers,omitempty"`
	// Models optionally lists model IDs offered by this provider, for
	// services that cannot enumerate their models.
	Models []string `yaml:"models,omitempty"`
	// APIPath optionally overrides the chat-completions path for
	// OpenAI-compatible servers with genuine path differences
	// (default "/chat/completions"). Discovery (GET /models) is unchanged.
	APIPath string `yaml:"api_path,omitempty"`
	// AuthMode selects how the resolved API key is sent: "" (auto:
	// bearer when a key resolves, none otherwise), "bearer", "none",
	// or "header" (custom header via AuthHeader). Empty preserves the
	// historic behavior.
	AuthMode string `yaml:"auth_mode,omitempty"`
	// AuthHeader names the custom header for AuthMode "header"
	// (e.g. "X-API-Key").
	AuthHeader string `yaml:"auth_header,omitempty"`
	// AuthScheme optionally overrides the credential prefix ("Bearer" by
	// default for bearer mode, raw key by default for header mode).
	AuthScheme string `yaml:"auth_scheme,omitempty"`
	// TimeoutSeconds optionally overrides the response-header timeout in
	// seconds. Zero keeps the built-in default (120s).
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty"`
	// Project is the Google Cloud project ID (Vertex AI).
	Project string `yaml:"project,omitempty"`
	// Location is the cloud region (Vertex AI location, e.g. us-central1).
	Location string `yaml:"location,omitempty"`
	// CredentialsFile optionally points at a service-account JSON key
	// used only for this provider (Vertex AI). Empty follows the
	// standard ADC chain instead.
	CredentialsFile string `yaml:"credentials_file,omitempty"`
	// Region is the AWS region (Bedrock, e.g. us-east-1). Falls back to
	// AWS_REGION/AWS_DEFAULT_REGION.
	Region string `yaml:"region,omitempty"`
	// Profile selects an AWS shared-config profile (Bedrock). Empty
	// follows the standard chain, including AWS_PROFILE.
	Profile string `yaml:"profile,omitempty"`
	// APIVersion pins the Azure data-plane api-version query parameter
	// (type azure only, e.g. "2024-06-01"). Empty keeps the adapter's
	// documented default.
	APIVersion string `yaml:"api_version,omitempty"`
	// AllowMetadataEndpoint explicitly opts in to using a cloud
	// instance-metadata URL as this provider's endpoint. Default false.
	// Only enable if you intentionally run a model server on such an
	// address; localhost and LAN addresses are unaffected.
	AllowMetadataEndpoint bool `yaml:"allow_metadata_endpoint,omitempty"`
	// Capabilities optionally forces capability flags for custom
	// endpoints that do not implement every OpenAI feature. Nil keeps
	// the adapter defaults.
	Capabilities *providers.CapabilitiesOverride `yaml:"capabilities,omitempty"`
}

// Agent describes the default agent's identity, base system prompt, and
// the limits that bound a single long-horizon run. Zero values fall back
// to runtime.DefaultLimits, so existing config files keep working
// unchanged.
type Agent struct {
	Name         string `yaml:"name"`
	SystemPrompt string `yaml:"system_prompt"`

	MaxIterations          int `yaml:"max_iterations,omitempty"`
	MaxToolCalls           int `yaml:"max_tool_calls,omitempty"`
	MaxConsecutiveFailures int `yaml:"max_consecutive_failures,omitempty"`

	// ContextWindow overrides the model's context window in tokens.
	// Zero (or omitted) resolves from the provider capability table,
	// falling back to message-count windowing for unknown models.
	ContextWindow int `yaml:"context_window,omitempty"`
	// ContextReserve overrides the tokens reserved for the next model
	// response. Zero falls back to the model's approximate output
	// limit, then to a conservative default.
	ContextReserve int `yaml:"context_reserve,omitempty"`
	// MaxContextMessages overrides how many history messages (excluding
	// the system prompt) are sent to the provider. Zero keeps the
	// built-in default.
	MaxContextMessages int `yaml:"max_context_messages,omitempty"`
	// ContextSummary, when true, replaces evicted middle turns with a
	// compact deterministic digest instead of dropping them silently.
	// Default false preserves the historic drop-middle behavior.
	ContextSummary bool `yaml:"context_summary,omitempty"`
}

// Permissions configures whether tool invocations run automatically or
// require interactive approval. Default applies to any tool without an
// explicit entry in Tools. Valid values (for Default and every entry in
// Tools) are "allow", "deny", and "ask"; see internal/permissions for how
// they're interpreted.
type Permissions struct {
	Default string            `yaml:"default"`
	Tools   map[string]string `yaml:"tools"`
}

// Sandbox configures the shell execution boundary. See docs/Sandbox.md.
type Sandbox struct {
	Mode     string          `yaml:"mode"`
	WSL      SandboxWSL      `yaml:"wsl"`
	Isolated SandboxIsolated `yaml:"isolated"`
}

// SandboxIsolated holds the Linux-only isolated-mode settings: extra
// paths granted beyond the built-in policy (read-write workspace and
// private tmp, read-only system paths). Empty means the built-in
// policy only. Used only when sandbox.mode is "isolated".
type SandboxIsolated struct {
	// Read grants extra read-only paths.
	Read []string `yaml:"read"`
	// Write grants extra writable paths.
	Write []string `yaml:"write"`
}

// SandboxWSL holds the WSL-specific sandbox settings.
type SandboxWSL struct {
	// Distribution selects the WSL distribution ("": system default).
	Distribution string `yaml:"distribution"`
	// Network is the requested network policy: "disabled" (default,
	// enforced via an in-distribution network namespace when possible -
	// commands refuse to run when it cannot be established) or "host"
	// (inherit WSL/host networking; never isolated).
	Network string `yaml:"network"`
}

// AgentConfig overrides one built-in agent (non-empty scalars replace;
// nil lists keep, explicit lists replace). Permissions stay global.
// See docs/Config.md and docs/Agents.md.
type AgentConfig struct {
	Description  string   `yaml:"description,omitempty"`
	SystemPrompt string   `yaml:"system_prompt,omitempty"`
	Tools        []string `yaml:"tools,omitempty"`
	Skills       []string `yaml:"skills,omitempty"`
	Constraints  []string `yaml:"constraints,omitempty"`
	Provider     string   `yaml:"provider,omitempty"`
	Model        string   `yaml:"model,omitempty"`

	MaxIterations          int   `yaml:"max_iterations,omitempty"`
	MaxToolCalls           int   `yaml:"max_tool_calls,omitempty"`
	MaxConsecutiveFailures int   `yaml:"max_consecutive_failures,omitempty"`
	ContextWindow          int   `yaml:"context_window,omitempty"`
	ContextReserve         int   `yaml:"context_reserve,omitempty"`
	MaxContextMessages     int   `yaml:"max_context_messages,omitempty"`
	ContextSummary         *bool `yaml:"context_summary,omitempty"`
}

// ToolLimits overrides output and execution bounds for one built-in
// tool. Every field is optional; omitted (or zero) values resolve to the
// tool's default from the shared limits table. Timeouts above 300s are
// rejected: the scheduler enforces 300s as the hard execution ceiling.
type ToolLimits struct {
	MaxBytes       int     `yaml:"max_bytes,omitempty"`
	MaxLines       int     `yaml:"max_lines,omitempty"`
	TimeoutSeconds float64 `yaml:"timeout_seconds,omitempty"`
}

// Workspace declares the project root and strictness. See docs/Sandbox.md;
// empty root resolves to Git top-level else cwd, empty mode is permissive.
type Workspace struct {
	Root string `yaml:"root,omitempty"`
	Mode string `yaml:"mode,omitempty"`
}

// Workspace modes.
const (
	WorkspacePermissive = "permissive"
	WorkspaceStrict     = "strict"
)

// ParseWorkspaceMode converts a workspace mode string: empty means
// permissive, preserving behavior for configs that never set it.
func ParseWorkspaceMode(s string) (string, error) {
	switch s {
	case "", WorkspacePermissive:
		return WorkspacePermissive, nil
	case WorkspaceStrict:
		return WorkspaceStrict, nil
	default:
		return "", fmt.Errorf("unknown workspace mode %q (supported: \"permissive\", \"strict\")", s)
	}
}

// Tracing controls the local-only execution trace (internal/trace):
// append-oriented JSONL under .forcefield/traces/, disabled unless
// explicitly enabled. Traces never leave the machine.
type Tracing struct {
	Enabled bool   `yaml:"enabled,omitempty"`
	Dir     string `yaml:"dir,omitempty"`
}

// Orchestration configures secure multi-agent delegation: a parent agent
// running child agent runs via the delegate tool. The feature is off by
// default; every bound exists so delegation can never recurse or fan out
// without explicit operator configuration. See docs/Agents.md.
//
// Omitted bounds keep their defaults from the runtime resolver; explicit
// values must fall inside the absolute ceilings below (validate rejects
// anything outside them at load, mirroring the ToolLimits timeout rule).
type Orchestration struct {
	// Enabled gates the delegate tool. Off (default) means no delegation
	// capability exists anywhere: agents naming "delegate" fail config
	// load with "unknown tool", which is fail-closed.
	Enabled bool `yaml:"enabled,omitempty"`
	// MaxDepth caps delegation nesting. Omitted resolves to 1 (children
	// only; grandchildren impossible). Absolute ceiling: 3. An explicit
	// value must be 1..3; explicit 0 is invalid (use the default by
	// omitting the field).
	MaxDepth *int `yaml:"max_depth,omitempty"`
	// MaxConcurrentChildren caps child runs running at once. Omitted
	// resolves to 4 (same as the tool scheduler default). Ceiling: 8.
	MaxConcurrentChildren *int `yaml:"max_concurrent_children,omitempty"`
	// MaxChildrenPerRun caps total child spawns per parent run (all
	// levels counted against the top run). Omitted resolves to 16.
	// Ceiling: 64.
	MaxChildrenPerRun *int `yaml:"max_children_per_run,omitempty"`
	// MaxTaskChars caps one delegation task payload. Omitted resolves to
	// 8000. Absolute ceiling: 65536; floor: 256.
	MaxTaskChars *int `yaml:"max_task_chars,omitempty"`
	// MaxTotalChildTurns caps aggregate model turns across ALL children
	// of one top-level run (every nesting level counts against the same
	// pool, so nesting cannot multiply the budget). Omitted resolves to
	// 2048. When the pool runs out, the offending child's context is
	// cancelled honestly (its own events record the cancellation) and
	// the exhaustion is reported once. Ceiling: 8192.
	MaxTotalChildTurns *int `yaml:"max_total_child_turns,omitempty"`
	// ChildPermissions is how child agents resolve permission prompts:
	// "deny" (default/equivalent, fail-closed) or "interactive" (child
	// asks route to the operator's real approval surface, attributed,
	// never persisted). Unknown values are rejected at load.
	ChildPermissions string `yaml:"child_permissions,omitempty"`
}

// Child permission modes.
const (
	ChildPermissionsDeny        = "deny"
	ChildPermissionsInteractive = "interactive"
)

// Orchestration ceilings. Anything above is rejected at load so an
// under-specified value can never widen the footprint.
const (
	MaxOrchestrationDepth     = 3
	MaxOrchestrationChildren  = 8
	MaxOrchestrationPerRun    = 64
	MinOrchestrationTaskChars = 256
	MaxOrchestrationTaskChars = 65536
	MaxOrchestrationTurns     = 8192
)

// Config is the top-level shape of config.yaml.
type Config struct {
	Model         Model                     `yaml:"model"`
	Providers     map[string]ProviderConfig `yaml:"providers,omitempty"`
	Agent         Agent                     `yaml:"agent"`
	Agents        map[string]AgentConfig    `yaml:"agents,omitempty"`
	Permissions   Permissions               `yaml:"permissions"`
	Sandbox       Sandbox                   `yaml:"sandbox"`
	Tools         map[string]ToolLimits     `yaml:"tools,omitempty"`
	Workspace     Workspace                 `yaml:"workspace,omitempty"`
	Tracing       Tracing                   `yaml:"tracing,omitempty"`
	Orchestration Orchestration             `yaml:"orchestration,omitempty"`
	// MCP holds local stdio MCP server definitions. The shape and
	// per-server bounds live in internal/mcp (the sole MCP owner); this
	// package owns file I/O, error wording, and persistence. Omitted
	// means zero servers and preserves existing behavior exactly.
	MCP mcp.Config `yaml:"mcp,omitempty"`
}

const defaultConfigTemplate = `model:
  provider: ollama
  endpoint: http://localhost:11434
  name: ornith:9b

agent:
  name: default
  system_prompt: |
    You are Forcefield, a local-first coding agent. Complete software tasks in real repositories: inspect, change, run, debug, and verify. Prefer a working, minimal result over advice or extra architecture.

permissions:
  default: ask

  tools:
    read_file: allow
    list_files: allow
    pwd: allow
    search_files: allow
    search_code: allow
    find_files: allow
    git: allow
    secret_scan: allow
    write_file: ask
    shell: ask
    shell_job: ask
    add_project_memory: ask

sandbox:
  # Execution boundary for shell commands.
  #   native - no isolation (historical behavior; commands run with your
  #            user's permissions and the full host environment).
  #   wsl    - run shell commands inside a WSL distribution under a
  #            restricted policy. Requires Windows; Forcefield refuses to
  #            run commands if WSL is unavailable rather than falling
  #            back. See docs/Sandbox.md for exactly what is enforced
  #            (pinned working directory, restricted environment, optional
  #            network namespace) and what is not (general filesystem
  #            confinement).
  mode: native
  wsl:
    distribution: ""    # empty = the system default distribution
    network: disabled   # "disabled" (enforced when possible, else refused) or "host"

workspace:
  # Project root for filesystem tools and shell working directories.
  #   root: ""          # empty = Git top-level when available, else cwd
  #   mode: permissive  # "permissive" (filesystem tools confined, shell unconfined) or "strict"
  # Filesystem tools are always confined to the root; strict additionally
  # pins the shell working directory to it; see docs/Sandbox.md.
  # Old configs without this block stay permissive.
  root: ""
  mode: permissive
`

// Dir returns the Forcefield home directory (~/.forcefield), creating it
// if necessary.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".forcefield")
	if info, err := os.Stat(dir); err == nil {
		// Fast path: the directory already exists, so skip the
		// create+chmod syscalls. Permissions are still repaired when
		// they actually differ (e.g. a directory from an older
		// version with a more permissive mode).
		if !info.IsDir() {
			return "", fmt.Errorf("create forcefield home %s: not a directory", dir)
		}
		if info.Mode().Perm() != 0o700 {
			if err := os.Chmod(dir, 0o700); err != nil {
				return "", fmt.Errorf("set permissions on %s: %w", dir, err)
			}
		}
		return dir, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat forcefield home %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create forcefield home %s: %w", dir, err)
	}
	// Ensure restrictive permissions even if the umask left the fresh
	// directory more permissive.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("set permissions on %s: %w", dir, err)
	}
	return dir, nil
}

// Path returns the path to config.yaml under the Forcefield home directory.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads config.yaml, creating it with default contents on first run.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(defaultConfigTemplate), 0o600); err != nil {
			return nil, fmt.Errorf("write default config to %s: %w", path, err)
		}
		// Ensure restrictive permissions even if umask is permissive.
		_ = os.Chmod(path, 0o600)
		// Stderr, not stdout: ff run emits machine-readable response
		// content on stdout, and a creation notice there would corrupt
		// scripts piping `ff run`.
		fmt.Fprintf(os.Stderr, "Created default config at %s\n", path)
	} else if err != nil {
		return nil, fmt.Errorf("stat config file %s: %w", path, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", path, err)
	}

	// validate resolves the active provider once (also surfacing
	// unknown types and missing endpoints early) and returns it, so
	// Load does not resolve the same provider a second time just to
	// populate the legacy APIKey convenience field.
	resolved, err := cfg.validate()
	if err != nil {
		return nil, fmt.Errorf("invalid config at %s: %w", path, err)
	}

	// Populate the legacy convenience field with the active provider's
	// resolved key (if any). The authoritative per-provider resolution is
	// ResolveProvider; this only keeps existing readers working.
	cfg.Model.APIKey = resolved.APIKey

	return &cfg, nil
}

// apiKeyName is the environment variable / .env key Forcefield reads its
// NVIDIA NIM API key from. Kept for compatibility with existing setups;
// per-provider keys are resolved through ResolveProvider instead.
const apiKeyName = "NVIDIA_API_KEY"

// ResolveEnvValue returns a credential value (env, then project .env, then
// global .env). Found values stay in-memory only, never enter os.Environ,
// so shell children cannot inherit them. Malformed .env fails closed.
// See docs/Config.md.
func ResolveEnvValue(name string) (value, source string, err error) {
	if v := os.Getenv(name); v != "" {
		v = strings.TrimSpace(v)
		// This lookup exists only for credential variables (api_key_env
		// and the legacy key): register the exact value so a key echoed
		// back by tool output, errors, or diagnostics is redacted
		// everywhere the centralized scrub runs.
		redact.AddSecret(v)
		return v, "environment", nil
	}

	// Project-local .env wins over the global one; the environment wins
	// over both.
	candidates := []string{filepath.Join(".", ".env")}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		candidates = append(candidates, filepath.Join(home, ".forcefield", ".env"))
	}

	for _, path := range candidates {
		data, readErr := os.ReadFile(path)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return "", "", fmt.Errorf("read %s: %w", path, readErr)
		}

		values, parseErr := parseDotEnv(string(data))
		if parseErr != nil {
			return "", "", fmt.Errorf("%s: %w", path, parseErr)
		}
		if v := values[name]; v != "" {
			redact.AddSecret(v)
			return v, fmt.Sprintf(".env file %s", path), nil
		}
	}
	return "", "", nil
}

// parseDotEnv parses the small .env subset Forcefield needs: KEY=VALUE
// lines, blank lines, and # comments. Values may be wrapped in single or
// double quotes. Anything else is reported as a malformed line so a typo
// never silently disables a setting the user believes is active.
func parseDotEnv(body string) (map[string]string, error) {
	values := make(map[string]string)
	for i, line := range strings.Split(body, "\n") {
		lineNo := i + 1
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("line %d is malformed (expected KEY=VALUE): %q", lineNo, line)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" {
			return nil, fmt.Errorf("line %d has an empty variable name", lineNo)
		}
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[name] = value
	}
	return values, nil
}

// Save writes config.yaml atomically (API keys never persisted).
// See docs/Config.md.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}

	out, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := writeFileAtomic(path, out, 0o600); err != nil {
		return fmt.Errorf("write config file %s: %w", path, err)
	}
	return nil
}

// writeFileAtomic writes data to path via a temporary file in the same
// directory that is flushed and then renamed over the destination. The
// rename is retried briefly because Windows can transiently refuse to
// replace a destination another handle still references (antivirus,
// indexing) - see session.replaceFile for the same rationale.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	// Preserve existing permissions when overwriting, otherwise use perm.
	// This avoids accidentally making a private file world-readable.
	targetPerm := perm
	if info, err := os.Stat(path); err == nil {
		targetPerm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat destination: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	// Ensure temp file is restrictive from the start.
	_ = os.Chmod(tmpName, 0o600)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write data: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush data: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, targetPerm); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}

	var renameErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 5 * time.Millisecond)
		}
		if renameErr = os.Rename(tmpName, path); renameErr == nil {
			return nil
		}
	}
	return fmt.Errorf("replace file: %w", renameErr)
}

// validate performs minimal sanity checks so failures surface early with a
// clear message instead of deep inside an HTTP call. It returns the
// resolved active provider so Load can populate the legacy APIKey field
// without resolving (and re-reading .env files) a second time.
func (c *Config) validate() (ResolvedProvider, error) {
	if c.Model.Provider == "" {
		return ResolvedProvider{}, fmt.Errorf("model.provider is required (e.g. \"ollama\")")
	}
	if c.Model.Name == "" {
		return ResolvedProvider{}, fmt.Errorf("model.name is required (e.g. \"llama3\")")
	}
	if c.Model.Endpoint != "" {
		u, err := url.Parse(c.Model.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return ResolvedProvider{}, fmt.Errorf("model.endpoint %q must be an absolute http:// or https:// URL", c.Model.Endpoint)
		}
	}

	for id, entry := range c.Providers {
		if err := validateEntry(id, entry); err != nil {
			return ResolvedProvider{}, err
		}
	}

	// Resolve the active provider now so unknown types and missing
	// endpoints fail at startup with a clear pointer at the offending
	// field instead of on the first request.
	resolved, err := c.ResolveProvider(c.Model.Provider, "")
	if err != nil {
		return ResolvedProvider{}, fmt.Errorf("model.provider %q: %w", c.Model.Provider, err)
	}

	if err := validatePermissionValue("permissions.default", c.Permissions.Default); err != nil {
		return ResolvedProvider{}, err
	}
	for tool, value := range c.Permissions.Tools {
		if err := validatePermissionValue(fmt.Sprintf("permissions.tools.%s", tool), value); err != nil {
			return ResolvedProvider{}, err
		}
	}

	if _, err := sandbox.ParseMode(c.Sandbox.Mode); err != nil {
		return ResolvedProvider{}, fmt.Errorf("sandbox.mode: %w", err)
	}
	if _, err := sandbox.ParseNetwork(c.Sandbox.WSL.Network); err != nil {
		return ResolvedProvider{}, fmt.Errorf("sandbox.wsl.network: %w", err)
	}
	if c.Sandbox.Mode == string(sandbox.ModeWSL) &&
		c.Sandbox.WSL.Distribution != "" && !sandbox.ValidDistroName(c.Sandbox.WSL.Distribution) {
		return ResolvedProvider{}, fmt.Errorf("sandbox.wsl.distribution %q is invalid (allowed: letters, digits, '.', '_', '-', and it may not start with '-')",
			c.Sandbox.WSL.Distribution)
	}

	if err := validateAgents(c.Agents); err != nil {
		return ResolvedProvider{}, err
	}

	if err := validateRunLimits("agent",
		c.Agent.MaxIterations, c.Agent.MaxToolCalls, c.Agent.MaxConsecutiveFailures,
		c.Agent.ContextWindow, c.Agent.ContextReserve, c.Agent.MaxContextMessages); err != nil {
		return ResolvedProvider{}, err
	}

	if err := validateTools(c.Tools); err != nil {
		return ResolvedProvider{}, err
	}

	// MCP server definitions are validated for shape only here: no
	// executables are resolved and no processes start at config load, so
	// doctor-without-spawning stays possible. Bounds live in internal/mcp;
	// its errors already carry the mcp: field paths.
	if err := c.MCP.Validate(); err != nil {
		return ResolvedProvider{}, err
	}

	if _, err := ParseWorkspaceMode(c.Workspace.Mode); err != nil {
		return ResolvedProvider{}, fmt.Errorf("workspace.mode: %w", err)
	}

	if err := validateOrchestration(c.Orchestration); err != nil {
		return ResolvedProvider{}, err
	}

	// delegate is a known tool name, but the capability only exists when
	// the orchestrator is built (enabled). Name it while disabled and the
	// tool-universe build would later reject the whole config with the
	// misleading "unknown tool" error; the same misconfiguration surfaces
	// here with a truthful, actionable message instead (fail-closed).
	if !c.Orchestration.Enabled {
		if err := validateDelegateNeedsOrchestration(c.Agents); err != nil {
			return ResolvedProvider{}, err
		}
	}

	return resolved, nil
}

// validateOrchestration rejects out-of-range or unknown delegation
// settings at load time so a bad value can never silently widen the
// orchestration footprint. Disabled orchestration validates too: the
// bounds are still checked so enabling later cannot surprise.
func validateOrchestration(o Orchestration) error {
	if o.MaxDepth != nil {
		if *o.MaxDepth < 1 || *o.MaxDepth > MaxOrchestrationDepth {
			return fmt.Errorf("orchestration.max_depth %d must be 1..%d (omit the field for the default)", *o.MaxDepth, MaxOrchestrationDepth)
		}
	}
	if o.MaxConcurrentChildren != nil {
		if *o.MaxConcurrentChildren < 1 || *o.MaxConcurrentChildren > MaxOrchestrationChildren {
			return fmt.Errorf("orchestration.max_concurrent_children %d must be 1..%d (omit the field for the default)", *o.MaxConcurrentChildren, MaxOrchestrationChildren)
		}
	}
	if o.MaxChildrenPerRun != nil {
		if *o.MaxChildrenPerRun < 1 || *o.MaxChildrenPerRun > MaxOrchestrationPerRun {
			return fmt.Errorf("orchestration.max_children_per_run %d must be 1..%d (omit the field for the default)", *o.MaxChildrenPerRun, MaxOrchestrationPerRun)
		}
	}
	// Cross-field: every nesting level needs its own child slot at the same
	// time (a depth-D chain holds D slots). cap < depth makes any
	// full-depth chain stall on the child semaphore until the delegate
	// timeout - rejected at load instead, naming both fields.
	if o.MaxDepth != nil && o.MaxConcurrentChildren != nil && *o.MaxConcurrentChildren < *o.MaxDepth {
		return fmt.Errorf(
			"orchestration.max_concurrent_children %d must be >= orchestration.max_depth %d (nested chains need one child slot per level)",
			*o.MaxConcurrentChildren, *o.MaxDepth)
	}
	if o.MaxTaskChars != nil {
		if *o.MaxTaskChars < MinOrchestrationTaskChars || *o.MaxTaskChars > MaxOrchestrationTaskChars {
			return fmt.Errorf("orchestration.max_task_chars %d must be %d..%d (omit the field for the default)", *o.MaxTaskChars, MinOrchestrationTaskChars, MaxOrchestrationTaskChars)
		}
	}
	if o.MaxTotalChildTurns != nil {
		if *o.MaxTotalChildTurns < 1 || *o.MaxTotalChildTurns > MaxOrchestrationTurns {
			return fmt.Errorf("orchestration.max_total_child_turns %d must be 1..%d (omit the field for the default)", *o.MaxTotalChildTurns, MaxOrchestrationTurns)
		}
	}
	switch o.ChildPermissions {
	case "", ChildPermissionsDeny, ChildPermissionsInteractive:
	default:
		return fmt.Errorf("orchestration.child_permissions %q is unknown (supported: %q, %q)", o.ChildPermissions, ChildPermissionsDeny, ChildPermissionsInteractive)
	}
	return nil
}

// validateDelegateNeedsOrchestration rejects agent tool overrides that name
// delegate while orchestration is disabled. The capability never exists in
// that state, so refusing the config is fail-closed; the point of doing it
// here (rather than at the tool-universe build) is the truthful error.
func validateDelegateNeedsOrchestration(agents map[string]AgentConfig) error {
	for name, cfg := range agents {
		for _, t := range cfg.Tools {
			if t == "delegate" {
				return fmt.Errorf("agents.%s.tools names delegate but orchestration.enabled is false - enable orchestration or drop the delegate entry", name)
			}
		}
	}
	return nil
}

// knownAgents is the set of built-in agent names for config validation.
// Kept here to avoid importing internal/agent (which would create a cycle
// in some test setups) and to make the error message deterministic.
var knownAgents = map[string]struct{}{
	"coding":   {},
	"cyber":    {},
	"legal":    {},
	"docs":     {},
	"research": {},
	"devops":   {},
	"general":  {},
}

func validateAgents(agents map[string]AgentConfig) error {
	for name, cfg := range agents {
		if _, ok := knownAgents[name]; !ok {
			return fmt.Errorf("agents.%s: unknown agent %q (available: coding, cyber, docs, devops, general, legal, research)", name, name)
		}
		if err := validateRunLimits("agents."+name,
			cfg.MaxIterations, cfg.MaxToolCalls, cfg.MaxConsecutiveFailures,
			cfg.ContextWindow, cfg.ContextReserve, cfg.MaxContextMessages); err != nil {
			return err
		}
		if cfg.Tools != nil {
			seen := make(map[string]struct{}, len(cfg.Tools))
			for i, t := range cfg.Tools {
				if strings.TrimSpace(t) == "" {
					return fmt.Errorf("agents.%s.tools[%d] is empty", name, i)
				}
				if _, dup := seen[t]; dup {
					return fmt.Errorf("agents.%s.tools: duplicate tool %q", name, t)
				}
				seen[t] = struct{}{}
			}
		}
		if cfg.Skills != nil {
			seen := make(map[string]struct{}, len(cfg.Skills))
			for i, s := range cfg.Skills {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("agents.%s.skills[%d] is empty", name, i)
				}
				if _, dup := seen[s]; dup {
					return fmt.Errorf("agents.%s.skills: duplicate skill %q", name, s)
				}
				seen[s] = struct{}{}
			}
		}
		if cfg.Constraints != nil {
			for i, c := range cfg.Constraints {
				if strings.TrimSpace(c) == "" {
					return fmt.Errorf("agents.%s.constraints[%d] is empty", name, i)
				}
			}
		}
		if cfg.Provider != "" && strings.TrimSpace(cfg.Provider) == "" {
			return fmt.Errorf("agents.%s.provider cannot be whitespace", name)
		}
		if cfg.Model != "" && strings.TrimSpace(cfg.Model) == "" {
			return fmt.Errorf("agents.%s.model cannot be whitespace", name)
		}
	}
	return nil
}

// knownTools is the set of tool names a tools: override block may
// address. Kept here (like knownAgents) so a typo fails fast at load
// instead of silently never applying.
var knownTools = map[string]struct{}{
	"read_file": {}, "write_file": {}, "list_files": {}, "pwd": {},
	"shell": {}, "shell_job": {}, "search_files": {}, "search_code": {}, "find_files": {}, "git": {}, "secret_scan": {},
	"load_skill": {}, "update_task_state": {}, "add_project_memory": {},
	"delegate": {},
}

// validateRunLimits rejects negative run/context bounds. Zero stays valid:
// it means "fall back to the default" at every resolution layer
// (limitsFromConfig, limitsForAgent, contextBudgetFromConfig all treat
// non-positive as unset), so only negatives are meaningless. CUE rejects
// them too; failing fast here keeps Go and CUE consistent instead of
// silently defaulting a typo into a bound the user never asked for.
func validateRunLimits(prefix string, maxIter, maxCalls, maxFail, ctxWindow, ctxReserve, ctxMsgs int) error {
	if maxIter < 0 {
		return fmt.Errorf("%s.max_iterations must not be negative (got %d)", prefix, maxIter)
	}
	if maxCalls < 0 {
		return fmt.Errorf("%s.max_tool_calls must not be negative (got %d)", prefix, maxCalls)
	}
	if maxFail < 0 {
		return fmt.Errorf("%s.max_consecutive_failures must not be negative (got %d)", prefix, maxFail)
	}
	if ctxWindow < 0 {
		return fmt.Errorf("%s.context_window must not be negative (got %d)", prefix, ctxWindow)
	}
	if ctxReserve < 0 {
		return fmt.Errorf("%s.context_reserve must not be negative (got %d)", prefix, ctxReserve)
	}
	if ctxMsgs < 0 {
		return fmt.Errorf("%s.max_context_messages must not be negative (got %d)", prefix, ctxMsgs)
	}
	return nil
}

func validateTools(entries map[string]ToolLimits) error {
	for name, lim := range entries {
		// Namespaced MCP tool overrides share the native limits
		// mechanism (same fields, same ceilings). Existence of the tool
		// is checked later at discovery time; limits can never disable
		// validation, permissions, output bounds, or redaction.
		if _, ok := knownTools[name]; !ok && !mcp.IsQualifiedToolName(name) {
			return fmt.Errorf("tools.%s: unknown tool %q (available: add_project_memory, find_files, git, list_files, load_skill, pwd, read_file, search_code, search_files, secret_scan, shell, shell_job, update_task_state, write_file)", name, name)
		}
		if lim.MaxBytes < 0 {
			return fmt.Errorf("tools.%s.max_bytes must be positive (got %d)", name, lim.MaxBytes)
		}
		if lim.MaxLines < 0 {
			return fmt.Errorf("tools.%s.max_lines must be positive (got %d)", name, lim.MaxLines)
		}
		if lim.TimeoutSeconds < 0 || lim.TimeoutSeconds > 300 {
			return fmt.Errorf("tools.%s.timeout_seconds must be between 0 and 300 (got %v)", name, lim.TimeoutSeconds)
		}
	}
	return nil
}

// depending on internal/permissions, which itself depends on this
// package to load and save config.yaml. "" is valid and means "ask".
func validatePermissionValue(field, value string) error {
	switch value {
	case "", "allow", "deny", "ask":
		return nil
	default:
		return fmt.Errorf("%s must be \"allow\", \"deny\", or \"ask\" (got %q)", field, value)
	}
}
