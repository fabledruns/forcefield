package cmd

import (
	"fmt"
	"strings"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/mcp"
)

// Phase 1: doctor renders sandbox facts from SummaryLines as info and
// limits from structured Limitations as warnings — never by substring.
// These tests pin that split plus the configured-distro and MCP
// UNSANDBOXED states.

func collectDoctorSandbox(t *testing.T, cfg *config.Config) ([]string, []verdict) {
	t.Helper()
	var lines []string
	var verdicts []verdict
	report := func(v verdict, format string, args ...any) {
		lines = append(lines, doctorLine(v, format, args...))
		verdicts = append(verdicts, v)
	}
	doctorSandbox(cfg, report)
	return lines, verdicts
}

func nativeSandboxConfig(dir string) *config.Config {
	return &config.Config{
		Model:     config.Model{Provider: "ollama", Name: "test-model"},
		Sandbox:   config.Sandbox{Mode: "native"},
		Workspace: config.Workspace{Root: dir},
	}
}

func TestDoctorSandbox_StructuredVerdicts(t *testing.T) {
	dir := t.TempDir()
	lines, verdicts := collectDoctorSandbox(t, nativeSandboxConfig(dir))
	if len(lines) == 0 {
		t.Fatal("expected sandbox lines for native config")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "sandbox Execution") {
		t.Errorf("missing Execution fact line:\n%s", joined)
	}
	// Facts containing NOT (e.g. "(other paths are NOT blocked)" in
	// strict/WSL, "NOT enforced" when unenforced) must NOT drive the
	// verdict by substring: fact lines render as ok, limits as warnings.
	for i, l := range lines {
		if strings.HasPrefix(l, "[ ok ] sandbox Execution") ||
			strings.HasPrefix(l, "[ ok ] sandbox Filesystem") ||
			strings.HasPrefix(l, "[ ok ] sandbox Network") ||
			strings.HasPrefix(l, "[ ok ] sandbox Environment") ||
			strings.HasPrefix(l, "[ ok ] sandbox Isolation") ||
			strings.HasPrefix(l, "[ ok ] sandbox Note") ||
			strings.HasPrefix(l, "[ ok ] sandbox distribution") {
			continue
		}
		if strings.Contains(l, "sandbox limitation [") {
			if verdicts[i] != vWarn {
				t.Errorf("limitation line must warn: %q", l)
			}
			if !strings.Contains(l, "]") {
				t.Errorf("limitation line must carry a stable ID: %q", l)
			}
			continue
		}
		t.Errorf("unexpected sandbox line (facts must be ok, limits must warn): %q", l)
	}
	foundLimitation := false
	for _, l := range lines {
		if strings.Contains(l, "sandbox limitation [") {
			foundLimitation = true
		}
	}
	if !foundLimitation {
		t.Errorf("native sandbox must report at least one structured limitation (shell-open):\n%s", joined)
	}
	_ = fmt.Sprint()
}

func TestDoctorShell_NamesConfiguredDistro(t *testing.T) {
	cfg := &config.Config{
		Model:     config.Model{Provider: "ollama", Name: "test-model"},
		Sandbox:   config.Sandbox{Mode: "wsl", WSL: config.SandboxWSL{Distribution: "Ubuntu-Test-XYZ"}},
		Workspace: config.Workspace{Root: t.TempDir()},
	}
	var lines []string
	report := func(v verdict, format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	doctorShell(cfg, report)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Ubuntu-Test-XYZ") {
		t.Errorf("doctorShell must name the configured distro, never silently probe another:\n%s", joined)
	}
}

func TestDoctorMCP_UnsandboxedWarn(t *testing.T) {
	dir := t.TempDir()
	withDoctorCwd(t, dir)
	cfg := doctorMCPConfig(map[string]mcp.ServerConfig{
		"demo": {Command: "/bin/true"},
	})
	lines, verdicts := collectDoctorMCP(t, cfg)
	joined := strings.Join(lines, "\n")
	found := false
	for i, l := range lines {
		if strings.Contains(l, "mcp.unsandboxed") && strings.Contains(l, "UNSANDBOXED") {
			found = true
			if verdicts[i] != vWarn {
				t.Errorf("MCP unsandboxed line must warn: %q", l)
			}
		}
	}
	if !found {
		t.Errorf("enabled MCP server must report mcp.unsandboxed UNSANDBOXED warning:\n%s", joined)
	}
}
