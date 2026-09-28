package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/runtime"
)

// TestMCPCommandsNilRuntime pins the no-runtime contract: every MCP
// Context method fails safe without a runtime instead of panicking.
func TestMCPCommandsNilRuntime(t *testing.T) {
	m := newTestModel()
	if got := m.MCPServers(); len(got) != 0 {
		t.Errorf("MCPServers() = %v, want empty without runtime", got)
	}
	if _, err := m.MCPServer("demo"); err == nil {
		t.Error("MCPServer accepted without runtime")
	}
	if err := m.MCPAddServer("demo", "/bin/demo", nil); err == nil {
		t.Error("MCPAddServer accepted without runtime")
	}
	if err := m.MCPRemoveServer("demo"); err == nil {
		t.Error("MCPRemoveServer accepted without runtime")
	}
	if err := m.MCPSetServerEnabled("demo", true); err == nil {
		t.Error("MCPSetServerEnabled accepted without runtime")
	}
	if _, err := m.MCPTestServer("demo"); err == nil {
		t.Error("MCPTestServer accepted without runtime")
	}
}

// TestMCPCommandRegistered keeps the /mcp command wired into the
// interactive registry: Lookup, tab-completion Match, and help metadata.
func TestMCPCommandRegistered(t *testing.T) {
	reg := newRegistry()
	cmd, ok := reg.Lookup("mcp")
	if !ok {
		t.Fatal("/mcp not registered")
	}
	if cmd.Name() != "mcp" || cmd.Description() == "" || cmd.Usage() == "" {
		t.Errorf("incomplete /mcp metadata: %+v", cmd)
	}
	found := false
	for _, cmd := range reg.Match("mc") {
		if cmd.Name() == "mcp" {
			found = true
		}
	}
	if !found {
		t.Errorf("Match(mc) misses /mcp")
	}
}

// mcpTestModel builds a model over a real runtime in an isolated home so
// the Context mapping (runtime state -> command DTOs) is covered without
// spawning any server: nothing configured is enabled at construction, and
// add/remove/enable never start servers.
func mcpTestModel(t *testing.T) model {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	rt, err := runtime.New()
	if err != nil {
		t.Fatalf("runtime.New() = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return model{runtime: rt, registry: newRegistry()}
}

func TestMCPModelRoundTrip(t *testing.T) {
	m := mcpTestModel(t)
	if got := m.MCPServers(); len(got) != 0 {
		t.Fatalf("MCPServers() = %v, want empty", got)
	}
	if err := m.MCPAddServer("demo", "/usr/local/bin/demo", []string{"--stdio"}); err != nil {
		t.Fatalf("MCPAddServer: %v", err)
	}
	servers := m.MCPServers()
	if len(servers) != 1 {
		t.Fatalf("MCPServers() = %v, want one server", servers)
	}
	s := servers[0]
	if s.Name != "demo" || !s.Enabled || s.Command != "/usr/local/bin/demo" {
		t.Errorf("mapping = %+v", s)
	}
	if len(s.Args) != 1 || s.Args[0] != "--stdio" {
		t.Errorf("args = %v", s.Args)
	}
	if s.State != "unknown" {
		t.Errorf("state = %q, want unknown (never started)", s.State)
	}
	got, err := m.MCPServer("demo")
	if err != nil {
		t.Fatalf("MCPServer: %v", err)
	}
	if got.Name != "demo" {
		t.Errorf("MCPServer = %+v", got)
	}
	if _, err := m.MCPServer("nope"); err == nil {
		t.Error("MCPServer accepted unknown name")
	}
	// Persistence goes to the isolated home, not the real config.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := reloaded.GetMCPServer("demo"); err != nil {
		t.Errorf("added server not persisted: %v", err)
	}

	if err := m.MCPSetServerEnabled("demo", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := m.MCPServers()[0]; got.State != "disabled" || got.Enabled {
		t.Errorf("after disable: %+v", got)
	}
	if _, err := m.MCPTestServer("demo"); err == nil {
		t.Error("test accepted a disabled server")
	} else if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled test error = %v, want a disabled hint", err)
	}
	if err := m.MCPSetServerEnabled("demo", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := m.MCPTestServer("nope"); err == nil {
		t.Error("test accepted an unknown server")
	}
	if err := m.MCPRemoveServer("demo"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := m.MCPServers(); len(got) != 0 {
		t.Errorf("MCPServers() = %v, want empty after remove", got)
	}
	if err := m.MCPRemoveServer("demo"); err == nil {
		t.Error("double remove accepted")
	}
}

// TestMCPModelTestServerFailure runs the ephemeral probe against a bogus
// command: it must fail fast locally (no hang, no session mutation) and
// report an error naming the server.
func TestMCPModelTestServerFailure(t *testing.T) {
	m := mcpTestModel(t)
	if err := m.MCPAddServer("bogus", "/nonexistent-binary-xyz", nil); err != nil {
		t.Fatalf("MCPAddServer: %v", err)
	}
	if _, err := m.MCPTestServer("bogus"); err == nil {
		t.Fatal("test accepted an unstartable server")
	} else if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("test error = %v, want the server name", err)
	}
	// The failed probe must not disturb configuration.
	if got := m.MCPServers(); len(got) != 1 || got[0].Name != "bogus" {
		t.Errorf("MCPServers() = %v after failed probe", got)
	}
}

// TestMCPModelConfigIsolated ensures the HOME override really redirects
// the config file into the throwaway dir, so model round trips never
// touch the user's real configuration.
func TestMCPModelConfigIsolated(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	path, err := config.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("config path = %s, want it inside %s", path, dir)
	}
}
