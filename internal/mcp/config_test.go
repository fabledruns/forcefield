package mcp

import (
	"errors"
	"strings"
	"testing"
)

func enabled(v bool) *bool { return &v }

func validServer() ServerConfig {
	return ServerConfig{Command: "/usr/local/bin/mcp-server"}
}

func TestConfigEmptyValid(t *testing.T) {
	if err := (Config{}).Validate(); err != nil {
		t.Errorf("empty config err = %v, want nil", err)
	}
}

func TestConfigValidServer(t *testing.T) {
	cfg := Config{Servers: map[string]ServerConfig{
		"filesystem": {
			Command:        "/usr/local/bin/mcp-filesystem",
			Args:           []string{"/data"},
			Env:            map[string]string{"FOO": "bar"},
			EnvPassthrough: []string{"PATH"},
			TimeoutSeconds: 30,
		},
	}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config err = %v, want nil", err)
	}
}

func TestConfigInvalidServerNames(t *testing.T) {
	for _, key := range []string{"", "has space", "has/slash", "has.dot", "UPPER!", "tool@x", strings.Repeat("k", MaxServerKeyLen+1)} {
		cfg := Config{Servers: map[string]ServerConfig{key: validServer()}}
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("key %q err = %v, want ErrInvalidConfig", key, err)
		}
	}
	boundary := Config{Servers: map[string]ServerConfig{strings.Repeat("k", MaxServerKeyLen): validServer()}}
	if err := boundary.Validate(); err != nil {
		t.Errorf("boundary key err = %v, want nil", err)
	}
}

func TestConfigMissingCommandWhenEnabled(t *testing.T) {
	for _, cfg := range []Config{
		{Servers: map[string]ServerConfig{"srv": {}}},
		{Servers: map[string]ServerConfig{"srv": {Command: "   "}}},
		{Servers: map[string]ServerConfig{"srv": {Command: "", Enabled: enabled(true)}}},
	} {
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("missing command err = %v, want ErrInvalidConfig", err)
		}
	}
}

func TestConfigDisabledServerSkipsCommand(t *testing.T) {
	cfg := Config{Servers: map[string]ServerConfig{"srv": {Enabled: enabled(false)}}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled without command err = %v, want nil", err)
	}
	// Shape still applies to disabled entries so typos surface early.
	bad := Config{Servers: map[string]ServerConfig{"srv": {Enabled: enabled(false), TimeoutSeconds: -1}}}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("disabled bad timeout err = %v, want ErrInvalidConfig", err)
	}
}

func TestConfigInvalidCommand(t *testing.T) {
	for _, cmd := range []string{
		"run; rm -rf /",
		"a | b",
		"a && b",
		"$(evil)",
		"`evil`",
		"line\nbreak",
		"tab\there",
		"nul\x00byte",
	} {
		cfg := Config{Servers: map[string]ServerConfig{"srv": {Command: cmd}}}
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("command %q err = %v, want ErrInvalidConfig", cmd, err)
		}
	}
	// Absolute paths with spaces and drive spellings stay legal.
	for _, cmd := range []string{
		`/usr/local/bin/mcp-server`,
		`C:\Program Files\mcp\server.exe`,
		`npx`,
		`./relative/bin`,
	} {
		cfg := Config{Servers: map[string]ServerConfig{"srv": {Command: cmd}}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("command %q err = %v, want nil", cmd, err)
		}
	}
}

func TestConfigOversizedArgs(t *testing.T) {
	big := ServerConfig{Command: "srv", Args: []string{strings.Repeat("a", MaxArgBytes+1)}}
	if err := (Config{Servers: map[string]ServerConfig{"s": big}}).Validate(); !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("oversized arg err = %v, want size error", err)
	}
	// Boundary element accepted.
	edge := ServerConfig{Command: "srv", Args: []string{strings.Repeat("a", MaxArgBytes)}}
	if err := (Config{Servers: map[string]ServerConfig{"s": edge}}).Validate(); err != nil {
		t.Errorf("boundary arg err = %v, want nil", err)
	}
	// Total overflow across small elements rejected.
	many := ServerConfig{Command: "srv"}
	for i := 0; i < MaxArgsTotalBytes/100+1; i++ {
		many.Args = append(many.Args, strings.Repeat("b", 100))
	}
	if err := (Config{Servers: map[string]ServerConfig{"s": many}}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("oversized argv total err = %v, want ErrInvalidConfig", err)
	}
	// NUL in args rejected (never modeled as inherited environ).
	nul := ServerConfig{Command: "srv", Args: []string{"ok\x00bad"}}
	if err := (Config{Servers: map[string]ServerConfig{"s": nul}}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("NUL arg err = %v, want ErrInvalidConfig", err)
	}
}

func TestConfigOversizedEnv(t *testing.T) {
	over := map[string]string{}
	for i := 0; i < MaxEnvEntries+1; i++ {
		over["K"+strings.Repeat("X", i)] = "v"
	}
	cfg := Config{Servers: map[string]ServerConfig{"s": {Command: "c", Env: over}}}
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("too many env entries err = %v, want ErrInvalidConfig", err)
	}
	bigVal := map[string]string{"OK": strings.Repeat("v", MaxEnvValueBytes+1)}
	cfg = Config{Servers: map[string]ServerConfig{"s": {Command: "c", Env: bigVal}}}
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("oversized env value err = %v, want ErrInvalidConfig", err)
	}
	badKey := map[string]string{"has-dash": "v"}
	cfg = Config{Servers: map[string]ServerConfig{"s": {Command: "c", Env: badKey}}}
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("bad env key err = %v, want ErrInvalidConfig", err)
	}
	// Boundary value accepted.
	edge := map[string]string{"OK": strings.Repeat("v", MaxEnvValueBytes)}
	cfg = Config{Servers: map[string]ServerConfig{"s": {Command: "c", Env: edge}}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("boundary env value err = %v, want nil", err)
	}
}

func TestConfigInvalidTimeout(t *testing.T) {
	for _, secs := range []float64{-1, -0.5, MaxServerTimeoutSeconds + 1, 1e9} {
		cfg := Config{Servers: map[string]ServerConfig{"s": {Command: "c", TimeoutSeconds: secs}}}
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("timeout %v err = %v, want ErrInvalidConfig", secs, err)
		}
	}
	for _, secs := range []float64{0, 0.5, 30, MaxServerTimeoutSeconds} {
		cfg := Config{Servers: map[string]ServerConfig{"s": {Command: "c", TimeoutSeconds: secs}}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("timeout %v err = %v, want nil", secs, err)
		}
	}
}

func TestConfigInvalidPassthrough(t *testing.T) {
	dup := ServerConfig{Command: "c", EnvPassthrough: []string{"PATH", "PATH"}}
	if err := (Config{Servers: map[string]ServerConfig{"s": dup}}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("duplicate passthrough err = %v, want ErrInvalidConfig", err)
	}
	bad := ServerConfig{Command: "c", EnvPassthrough: []string{"9LIVES"}}
	if err := (Config{Servers: map[string]ServerConfig{"s": bad}}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("bad passthrough name err = %v, want ErrInvalidConfig", err)
	}
	many := ServerConfig{Command: "c"}
	for i := 0; i < MaxPassthroughEntries+1; i++ {
		many.EnvPassthrough = append(many.EnvPassthrough, "V"+strings.Repeat("A", i))
	}
	if err := (Config{Servers: map[string]ServerConfig{"s": many}}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("too many passthrough err = %v, want ErrInvalidConfig", err)
	}
	ok := ServerConfig{Command: "c", EnvPassthrough: []string{"PATH", "HOME"}}
	if err := (Config{Servers: map[string]ServerConfig{"s": ok}}).Validate(); err != nil {
		t.Errorf("valid passthrough err = %v, want nil", err)
	}
}

func TestConfigMaxServers(t *testing.T) {
	cfg := Config{Servers: map[string]ServerConfig{}}
	for i := 0; i < MaxServers; i++ {
		cfg.Servers["srv"+string(rune('a'+i))] = validServer()
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("max servers err = %v, want nil", err)
	}
	cfg.Servers["one-too-many"] = validServer()
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("over max servers err = %v, want ErrInvalidConfig", err)
	}
	// Disabled entries do not consume the budget.
	cfg.Servers["one-too-many"] = ServerConfig{Enabled: enabled(false)}
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled over-count err = %v, want nil", err)
	}
}

func TestIsEnabled(t *testing.T) {
	if !(ServerConfig{}).IsEnabled() {
		t.Error("zero ServerConfig must default to enabled")
	}
	if !(ServerConfig{Enabled: enabled(true)}).IsEnabled() {
		t.Error("explicit true must be enabled")
	}
	if (ServerConfig{Enabled: enabled(false)}).IsEnabled() {
		t.Error("explicit false must be disabled")
	}
}

func TestIsEnvName(t *testing.T) {
	for _, n := range []string{"PATH", "_x", "A1", "a_b_c9"} {
		if !IsEnvName(n) {
			t.Errorf("IsEnvName(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"", "9LIVES", "has-dash", "has space", "dot.name"} {
		if IsEnvName(n) {
			t.Errorf("IsEnvName(%q) = true, want false", n)
		}
	}
}
