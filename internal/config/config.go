// Package config loads Forcefield's YAML configuration file.
//
// Config lives at ~/.forcefield/config.yaml. If it doesn't exist, Load
// writes a sensible default file and directory structure so a first-time
// user always has something working to edit.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"forcefield/internal/sandbox"
)

// Model describes how to reach a local model provider.
type Model struct {
	Provider string `yaml:"provider"`
	Endpoint string `yaml:"endpoint"`
	Name     string `yaml:"name"`
	APIKey   string `yaml:"-"`
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

// Sandbox configures the execution boundary for shell commands.
//
// Mode "" means "native": historical behavior with NO isolation, kept so
// existing users are unaffected until they opt in. "wsl" executes shell
// commands inside a WSL distribution under a restricted policy; see
// internal/sandbox and docs/Sandbox.md for exactly what is enforced and
// what is not.
type Sandbox struct {
	Mode string     `yaml:"mode"`
	WSL  SandboxWSL `yaml:"wsl"`
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

// Config is the top-level shape of config.yaml.
type Config struct {
	Model       Model       `yaml:"model"`
	Agent       Agent       `yaml:"agent"`
	Permissions Permissions `yaml:"permissions"`
	Sandbox     Sandbox     `yaml:"sandbox"`
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
    write_file: ask
    shell: ask
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
`

// Dir returns the Forcefield home directory (~/.forcefield), creating it
// if necessary.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".forcefield")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create forcefield home %s: %w", dir, err)
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
		if err := os.WriteFile(path, []byte(defaultConfigTemplate), 0o644); err != nil {
			return nil, fmt.Errorf("write default config to %s: %w", path, err)
		}
		fmt.Printf("Created default config at %s\n", path)
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

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config at %s: %w", path, err)
	}

	key, _, err := ResolveAPIKey()
	if err != nil {
		return nil, err
	}
	cfg.Model.APIKey = key

	return &cfg, nil
}

// apiKeyName is the environment variable / .env key Forcefield reads its
// NVIDIA NIM API key from.
const apiKeyName = "NVIDIA_API_KEY"

// ResolveAPIKey returns the NVIDIA API key along with where it came from:
// the process environment first, then .env files (project-local, then
// ~/.forcefield/.env).
//
// A key found in .env is returned to the caller instead of being written
// back into the process environment on purpose: everything in the process
// environment is inherited by every command the shell tool runs, so
// importing .env contents into os.Environ would hand the key to every
// subprocess. Providers receive the key through the config struct, which
// is never persisted (yaml:"-") and never printed.
//
// A .env file that contains malformed non-comment lines is rejected with
// a named-file error rather than being partially applied.
func ResolveAPIKey() (key, source string, err error) {
	if v := os.Getenv(apiKeyName); v != "" {
		return strings.TrimSpace(v), "environment", nil
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

		value, parseErr := parseDotEnv(string(data))
		if parseErr != nil {
			return "", "", fmt.Errorf("%s: %w", path, parseErr)
		}
		if v := value[apiKeyName]; v != "" {
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

// Save writes the config back to config.yaml. APIKey is never
// round-tripped (it's tagged yaml:"-" and always sourced from the
// environment), so it's never written to disk.
//
// The write is atomic (temp file + rename + flush), so a crash or kill
// during a model/provider/permission switch can never leave a truncated
// config.yaml behind; readers see either the old or the new complete
// file.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}

	out, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := writeFileAtomic(path, out, 0o644); err != nil {
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
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

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
	if err := os.Chmod(tmpName, perm); err != nil {
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
// clear message instead of deep inside an HTTP call.
func (c *Config) validate() error {
	if c.Model.Provider == "" {
		return fmt.Errorf("model.provider is required (e.g. \"ollama\")")
	}
	if c.Model.Endpoint == "" {
		return fmt.Errorf("model.endpoint is required (e.g. \"http://localhost:11434\")")
	}
	if c.Model.Name == "" {
		return fmt.Errorf("model.name is required (e.g. \"llama3\")")
	}

	if err := validatePermissionValue("permissions.default", c.Permissions.Default); err != nil {
		return err
	}
	for tool, value := range c.Permissions.Tools {
		if err := validatePermissionValue(fmt.Sprintf("permissions.tools.%s", tool), value); err != nil {
			return err
		}
	}

	if _, err := sandbox.ParseMode(c.Sandbox.Mode); err != nil {
		return fmt.Errorf("sandbox.mode: %w", err)
	}
	if _, err := sandbox.ParseNetwork(c.Sandbox.WSL.Network); err != nil {
		return fmt.Errorf("sandbox.wsl.network: %w", err)
	}
	if c.Sandbox.Mode == string(sandbox.ModeWSL) &&
		c.Sandbox.WSL.Distribution != "" && !sandbox.ValidDistroName(c.Sandbox.WSL.Distribution) {
		return fmt.Errorf("sandbox.wsl.distribution %q is invalid (allowed: letters, digits, '.', '_', '-', and it may not start with '-')",
			c.Sandbox.WSL.Distribution)
	}

	return nil
}

// validatePermissionValue checks a raw permissions string without
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
