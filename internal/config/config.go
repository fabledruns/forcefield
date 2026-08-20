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

	"gopkg.in/yaml.v3"
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

// Config is the top-level shape of config.yaml.
type Config struct {
	Model       Model       `yaml:"model"`
	Agent       Agent       `yaml:"agent"`
	Permissions Permissions `yaml:"permissions"`
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

	cfg.Model.APIKey = os.Getenv("NVIDIA_API_KEY")

	return &cfg, nil
}

// Save writes the config back to config.yaml. APIKey is never
// round-tripped (it's tagged yaml:"-" and always sourced from the
// environment), so it's never written to disk.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}

	out, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write config file %s: %w", path, err)
	}
	return nil
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
