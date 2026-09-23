package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/mcp"
)

// TestDoctorMCPHelperProcess is re-executed as a fake MCP server child if
// doctor ever spawns one. It writes a sentinel file and exits cleanly, so
// the negative-spawn tests below fail loudly on the sentinel rather than
// on a hung or crashed child.
func TestDoctorMCPHelperProcess(t *testing.T) {
	if os.Getenv("FF_MCP_DOCTOR_CANARY") != "1" {
		return
	}
	if path := os.Getenv("FF_MCP_DOCTOR_CANARY_FILE"); path != "" {
		_ = os.WriteFile(path, []byte("spawned"), 0o600)
	}
	os.Exit(0)
}

func collectDoctorMCP(t *testing.T, cfg *config.Config) ([]string, []verdict) {
	t.Helper()
	var lines []string
	var verdicts []verdict
	report := func(v verdict, format string, args ...any) {
		// Mirror the production path: every line passes centralized
		// redaction before display.
		lines = append(lines, doctorLine(v, format, args...))
		verdicts = append(verdicts, v)
	}
	doctorMCP(cfg, report)
	return lines, verdicts
}

func withDoctorCwd(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func canaryServerConfig(t *testing.T, dir string, extraEnv map[string]string) mcp.ServerConfig {
	t.Helper()
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("helper exe: %v", err)
	}
	env := map[string]string{
		"FF_MCP_DOCTOR_CANARY":      "1",
		"FF_MCP_DOCTOR_CANARY_FILE": filepath.Join(dir, "canary"),
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	return mcp.ServerConfig{
		Command: exe,
		Args:    []string{"-test.run=^TestDoctorMCPHelperProcess$"},
		Env:     env,
	}
}

func doctorMCPConfig(servers map[string]mcp.ServerConfig) *config.Config {
	return &config.Config{
		Model: config.Model{Provider: "ollama", Name: "test-model"},
		MCP:   mcp.Config{Servers: servers},
	}
}

func TestDoctorMCPNoServers(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	lines, verdicts := collectDoctorMCP(t, doctorMCPConfig(nil))
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want one concise line", lines)
	}
	if !strings.Contains(lines[0], "no servers configured") {
		t.Errorf("line = %q", lines[0])
	}
	for _, v := range verdicts {
		if v == vFail {
			t.Errorf("no-server config must not fail: %v", lines)
		}
	}
}

func TestDoctorMCPValidNeverSpawns(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	sentinel := filepath.Join(dir, "canary")
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"demo": canaryServerConfig(t, dir, nil),
	})
	lines, verdicts := collectDoctorMCP(t, cfg)
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("doctor spawned the configured server (sentinel exists)")
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{`"demo"`, "never starts servers", "does not prove reachability"} {
		if !strings.Contains(joined, want) {
			t.Errorf("output lacks %q:\n%s", want, joined)
		}
	}
	for _, v := range verdicts {
		if v == vFail {
			t.Errorf("valid config must not fail: %v", lines)
		}
	}
}

func TestDoctorMCPDisabledNeverSpawns(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	off := false
	sentinel := filepath.Join(dir, "canary")
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"off": func() mcp.ServerConfig {
			sc := canaryServerConfig(t, dir, nil)
			sc.Enabled = &off
			return sc
		}(),
	})
	lines, _ := collectDoctorMCP(t, cfg)
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("doctor spawned a disabled server")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "disabled") {
		t.Errorf("disabled server not labeled: %v", lines)
	}
}

func TestDoctorMCPInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"bad key!": {Command: "/bin/x"},
	})
	lines, verdicts := collectDoctorMCP(t, cfg)
	sawFail := false
	for i, v := range verdicts {
		if v == vFail {
			sawFail = true
			if !strings.Contains(lines[i], "invalid configuration") {
				t.Errorf("fail line = %q", lines[i])
			}
		}
	}
	if !sawFail {
		t.Errorf("invalid shape must fail, got %v", lines)
	}
}

func TestDoctorMCPStaleStatus(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"demo": canaryServerConfig(t, dir, nil),
	})
	// Status written for a DIFFERENT configuration: must read stale.
	other := doctorMCPConfig(map[string]mcp.ServerConfig{
		"demo": canaryServerConfig(t, dir, map[string]string{"EXTRA": "1"}),
	})
	st := mcp.StatusFile{
		Version:      mcp.StatusVersion,
		ConfigSHA256: mcp.FingerprintConfig(other.MCP),
		UpdatedUnix:  1,
		Servers: []mcp.ServerStatus{
			{Key: "demo", Enabled: true, Ready: true, Healthy: true, Tools: []string{"mcp__demo__echo"}},
		},
	}
	if err := mcp.WriteStatusFile(dir, st); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}
	lines, _ := collectDoctorMCP(t, cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "stale") {
		t.Errorf("stale status not labeled:\n%s", joined)
	}
	if strings.Contains(joined, "(status current)") {
		t.Errorf("stale status presented as current:\n%s", joined)
	}
	if _, err := os.Stat(filepath.Join(dir, "canary")); !os.IsNotExist(err) {
		t.Fatal("stale-status doctor spawned a server")
	}
}

func TestDoctorMCPCurrentHealthyAndFailed(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"demo": canaryServerConfig(t, dir, nil),
		"bad":  canaryServerConfig(t, dir, nil),
	})
	st := mcp.StatusFile{
		Version:      mcp.StatusVersion,
		ConfigSHA256: mcp.FingerprintConfig(cfg.MCP),
		Servers: []mcp.ServerStatus{
			{Key: "demo", Enabled: true, Ready: true, Healthy: true, Tools: []string{"mcp__demo__echo", "mcp__demo__two"}},
			{Key: "bad", Enabled: true, Ready: false, Started: true, LastError: "boom: handshake failed"},
		},
	}
	if err := mcp.WriteStatusFile(dir, st); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}
	lines, _ := collectDoctorMCP(t, cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "healthy, 2 tool") {
		t.Errorf("healthy server misreported:\n%s", joined)
	}
	if !strings.Contains(joined, "boom: handshake failed") {
		t.Errorf("last-known failure missing:\n%s", joined)
	}
	if strings.Contains(joined, "stale") {
		t.Errorf("current status labeled stale:\n%s", joined)
	}
	if _, err := os.Stat(filepath.Join(dir, "canary")); !os.IsNotExist(err) {
		t.Fatal("status-reading doctor spawned a server")
	}
}

func TestDoctorMCPMissingAndCorruptStatus(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"demo": canaryServerConfig(t, dir, nil),
	})
	lines, _ := collectDoctorMCP(t, cfg)
	if !strings.Contains(strings.Join(lines, "\n"), "no status recorded yet") {
		t.Errorf("missing status not reported:\n%s", strings.Join(lines, "\n"))
	}
	if err := os.MkdirAll(filepath.Join(dir, ".forcefield"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcp.StatusFilePath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, _ = collectDoctorMCP(t, cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "unreadable") || !strings.Contains(joined, "rewritten") {
		t.Errorf("corrupt status mishandled:\n%s", joined)
	}
}

func TestDoctorMCPNeverLeaksSecrets(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	secret := "doctor-mcp-canary-token-abcdef-123456"
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"demo": canaryServerConfig(t, dir, map[string]string{"API_TOKEN": secret}),
	})
	// A status whose scrubbed diagnostic echoes a pattern-shaped secret:
	// centralized redaction must strip it from doctor output.
	st := mcp.StatusFile{
		Version:      mcp.StatusVersion,
		ConfigSHA256: mcp.FingerprintConfig(cfg.MCP),
		Servers: []mcp.ServerStatus{
			{Key: "demo", Enabled: true, Ready: false, Started: true,
				LastError: "dial failed echoing sk-" + strings.Repeat("A", 24)},
		},
	}
	if err := mcp.WriteStatusFile(dir, st); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}
	lines, _ := collectDoctorMCP(t, cfg)
	joined := strings.Join(lines, "\n")
	for _, needle := range []string{secret, "API_TOKEN", "sk-" + strings.Repeat("A", 24)} {
		if strings.Contains(joined, needle) {
			t.Errorf("doctor output leaks %q:\n%s", needle, joined)
		}
	}
	if !strings.Contains(joined, `"demo"`) {
		t.Errorf("server missing from output:\n%s", joined)
	}
}

func TestDoctorMCPNilConfig(t *testing.T) {
	called := false
	report := func(v verdict, format string, args ...any) {
		called = true
	}
	doctorMCP(nil, report)
	if called {
		t.Error("nil config must stay silent (config failure already reported)")
	}
}

func TestDoctorMCPBadCwd(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	sc := canaryServerConfig(t, dir, nil)
	sc.Cwd = filepath.Join(dir, "no-such-dir")
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{"demo": sc})
	lines, _ := collectDoctorMCP(t, cfg)
	if !strings.Contains(strings.Join(lines, "\n"), "does not exist") {
		t.Errorf("bad cwd not flagged:\n%s", strings.Join(lines, "\n"))
	}
}

func TestDoctorMCPTruncatedStatus(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"demo": canaryServerConfig(t, dir, nil),
	})
	if err := os.MkdirAll(filepath.Join(dir, ".forcefield"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Truncated mid-document: valid prefix, no closing brace. Must read
	// as corrupt (warn), never crash, never spawn.
	if err := os.WriteFile(mcp.StatusFilePath(dir), []byte(`{"version":1,"servers":[{"key":"demo"`), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, verdicts := collectDoctorMCP(t, cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "unreadable") {
		t.Errorf("truncated status mishandled:\n%s", joined)
	}
	for _, v := range verdicts {
		if v == vFail {
			t.Errorf("unreadable status must warn, not fail:\n%s", joined)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "canary")); !os.IsNotExist(err) {
		t.Fatal("doctor spawned a server while reading corrupt status")
	}
}

func TestDoctorMCPExcessiveStatusClamped(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"demo": canaryServerConfig(t, dir, nil),
	})
	// A hand-built file with absurd field sizes must still render bounded
	// output: the reader clamps, so doctor cannot echo it raw.
	var tools []string
	for i := 0; i < 300; i++ {
		tools = append(tools, "mcp__demo__tool")
	}
	st := mcp.StatusFile{
		Version:      mcp.StatusVersion,
		ConfigSHA256: mcp.FingerprintConfig(cfg.MCP),
		Servers: []mcp.ServerStatus{{
			Key: "demo", Enabled: true, Ready: false, Started: true,
			Tools:      tools,
			LastError:  strings.Repeat("e", mcp.MaxErrorDetailRunes+5000),
			StderrTail: strings.Repeat("s", mcp.MaxStatusStderrChars+5000),
		}},
	}
	if err := mcp.WriteStatusFile(dir, st); err != nil {
		t.Fatalf("WriteStatusFile: %v", err)
	}
	lines, _ := collectDoctorMCP(t, cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"demo"`) {
		t.Errorf("server missing from output:\n%s", joined)
	}
	for _, line := range lines {
		if len([]rune(line)) > 4096 {
			t.Errorf("doctor line of %d runes is unbounded", len([]rune(line)))
		}
	}
}
