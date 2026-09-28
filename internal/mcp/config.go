package mcp

import (
	"fmt"
	"math"
	"strings"
)

// Configuration shape and pure validation for mcp.servers. This file owns
// the canonical shape: internal/config embeds Config as its mcp: block so
// there is exactly one definition. Validation here is shape-only (bounds,
// charsets, counts, ranges): executable resolution, working-directory
// existence, and environment construction happen at startup in a later
// phase, never at config load, so doctor-without-spawning stays possible.
//
// Environment values are literal: no variable expansion exists in v1.
// The default environment is empty on Unix and SYSTEMROOT-only on
// Windows; operator passthrough is an explicit per-server allowlist.

const (
	// maxServerKeyRunes mirrors MaxServerKeyLen for messages.
	maxServerKeyRunes = MaxServerKeyLen
)

// ServerConfig is one entry under mcp.servers. Every field is optional in
// the file; Enabled absent means enabled, TimeoutSeconds zero means the
// default, and Cwd empty means the workspace root at startup.
type ServerConfig struct {
	// Command is the server executable: an absolute path or a name
	// resolved via LookPath at startup. Never interpreted by a shell.
	Command string `yaml:"command,omitempty"`
	// Args are argv elements passed directly, without expansion.
	Args []string `yaml:"args,omitempty"`
	// Cwd is the launch directory: empty selects the workspace root.
	Cwd string `yaml:"cwd,omitempty"`
	// Env holds literal extra variables overlaying passthrough copies.
	Env map[string]string `yaml:"env,omitempty"`
	// EnvPassthrough names host variables copied at spawn time. Empty by
	// default; unknown host variables are skipped, never an error.
	EnvPassthrough []string `yaml:"env_passthrough,omitempty"`
	// TimeoutSeconds bounds one tools/call attempt. Zero selects the
	// default; values above MaxServerTimeoutSeconds are rejected.
	TimeoutSeconds float64 `yaml:"timeout_seconds,omitempty"`
	// Enabled selects the server. Nil (absent) means enabled.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// Config is the top-level mcp: block: a map from server key to config.
type Config struct {
	Servers map[string]ServerConfig `yaml:"servers,omitempty"`
}

// IsEnabled reports whether s runs. Absent means enabled.
func (s ServerConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// Validate checks the whole block: the enabled-server budget plus every
// entry's shape. It performs no I/O and resolves no executables.
func (c Config) Validate() error {
	enabled := 0
	for _, s := range c.Servers {
		if s.IsEnabled() {
			enabled++
		}
	}
	if enabled > MaxServers {
		return fmt.Errorf("mcp: %d enabled servers exceed the maximum of %d: %w", enabled, MaxServers, ErrInvalidConfig)
	}
	for key, s := range c.Servers {
		if err := ValidateServerKey(key); err != nil {
			return err
		}
		if err := s.validate(key); err != nil {
			return err
		}
	}
	return nil
}

// ValidateServerKey checks one mcp.servers map key: 1-32 characters of
// letters, digits, underscore, hyphen. The bound keeps qualified tool
// names predictable and within provider grammars.
func ValidateServerKey(key string) error {
	n := len([]rune(key))
	if n < MinServerKeyLen || n > maxServerKeyRunes {
		return fmt.Errorf("mcp: server key %q must be %d-%d characters: %w",
			quoteBounded(key), MinServerKeyLen, maxServerKeyRunes, ErrInvalidConfig)
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return fmt.Errorf("mcp: server key %q contains invalid character %q (allowed: letters, digits, '_', '-'): %w",
				quoteBounded(key), string(r), ErrInvalidConfig)
		}
	}
	return nil
}

// validate checks one entry's shape. key names the entry for errors.
func (s ServerConfig) validate(key string) error {
	field := func(name string) string {
		return fmt.Sprintf("servers.%q.%s", key, name)
	}
	if s.IsEnabled() && strings.TrimSpace(s.Command) == "" {
		return configErrorf("%s: command is required when the server is enabled", field("command"))
	}
	if s.Command != "" {
		if err := checkCommand(s.Command); err != nil {
			return configErrorf("%s: %v", field("command"), err)
		}
	}
	total := 0
	for i, a := range s.Args {
		if len(a) > MaxArgBytes {
			return configErrorf("%s: args[%d] of %d bytes exceeds %d byte limit", field("args"), i, len(a), MaxArgBytes)
		}
		if strings.IndexByte(a, 0) >= 0 {
			return configErrorf("%s: args[%d] must not contain NUL", field("args"), i)
		}
		total += len(a)
	}
	if total > MaxArgsTotalBytes {
		return configErrorf("%s: args total of %d bytes exceeds %d byte limit", field("args"), total, MaxArgsTotalBytes)
	}
	if len(s.Cwd) > MaxCwdBytes {
		return configErrorf("%s: value of %d bytes exceeds %d byte limit", field("cwd"), len(s.Cwd), MaxCwdBytes)
	}
	if strings.IndexByte(s.Cwd, 0) >= 0 {
		return configErrorf("%s: value must not contain NUL", field("cwd"))
	}
	if len(s.Env) > MaxEnvEntries {
		return configErrorf("%s: %d entries exceed the maximum of %d", field("env"), len(s.Env), MaxEnvEntries)
	}
	for k, v := range s.Env {
		if !IsEnvName(k) {
			return configErrorf("%s: key %q is not a valid environment variable name", field("env"), quoteBounded(k))
		}
		if len(v) > MaxEnvValueBytes {
			return configErrorf("%s: value for %q of %d bytes exceeds %d byte limit", field("env"), quoteBounded(k), len(v), MaxEnvValueBytes)
		}
		if strings.IndexByte(v, 0) >= 0 {
			return configErrorf("%s: value for %q must not contain NUL", field("env"), quoteBounded(k))
		}
	}
	if len(s.EnvPassthrough) > MaxPassthroughEntries {
		return configErrorf("%s: %d entries exceed the maximum of %d", field("env_passthrough"), len(s.EnvPassthrough), MaxPassthroughEntries)
	}
	seen := make(map[string]struct{}, len(s.EnvPassthrough))
	for i, name := range s.EnvPassthrough {
		if !IsEnvName(name) {
			return configErrorf("%s: entry [%d] %q is not a valid environment variable name", field("env_passthrough"), i, quoteBounded(name))
		}
		if _, dup := seen[name]; dup {
			return configErrorf("%s: duplicate entry %q", field("env_passthrough"), quoteBounded(name))
		}
		seen[name] = struct{}{}
	}
	if err := checkTimeout(s.TimeoutSeconds); err != nil {
		return configErrorf("%s: %v", field("timeout_seconds"), err)
	}
	return nil
}

// checkCommand rejects empty commands and shell-control characters. The
// command is an executable path or bare name resolved at startup; it is
// never passed through a shell, so metacharacters indicate a mistake, not
// a feature. Spaces, colons, slashes, and drive-letter spellings stay
// legal so absolute paths (including Windows paths) keep working.
func checkCommand(cmd string) error {
	if len(cmd) > MaxCommandBytes {
		return fmt.Errorf("value of %d bytes exceeds %d byte limit: %w", len(cmd), MaxCommandBytes, ErrInvalidConfig)
	}
	for _, r := range cmd {
		switch {
		case r == 0 || r == '\n' || r == '\r':
			return fmt.Errorf("value must not contain control character %q: %w", r, ErrInvalidConfig)
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("value must not contain control character %q: %w", r, ErrInvalidConfig)
		case r == ';' || r == '|' || r == '&' || r == '$' || r == '(' || r == ')' || r == '`':
			return fmt.Errorf("value must not contain shell metacharacter %q (command is executed directly, never through a shell): %w", string(r), ErrInvalidConfig)
		}
	}
	return nil
}

// checkTimeout validates a per-server attempt timeout: zero selects the
// default, positive values up to the hard ceiling are accepted, and
// negatives, NaN, infinities, and over-ceiling values are rejected.
func checkTimeout(secs float64) error {
	if math.IsNaN(secs) || math.IsInf(secs, 0) {
		return fmt.Errorf("value must be a finite number of seconds: %w", ErrInvalidConfig)
	}
	if secs < 0 || secs > MaxServerTimeoutSeconds {
		return fmt.Errorf("value %v must be between 0 and %v: %w", secs, MaxServerTimeoutSeconds, ErrInvalidConfig)
	}
	return nil
}

// IsEnvName reports whether s is a plausible environment variable name:
// a letter or underscore followed by letters, digits, or underscores.
// It mirrors the provider configuration rule so both layers agree.
func IsEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
