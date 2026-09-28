package runtime

import (
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/mcp"
	"forcefield/internal/tools"
)

// fakeRuntimeWithMCP builds a minimal Runtime around an MCP host without
// touching the real forcefield home. Config saves go to a temp HOME.
func fakeRuntimeWithMCP(t *testing.T, cfg *config.Config, full *tools.Manager, host *mcp.Host, workspace string) *Runtime {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return &Runtime{
		cfg:           cfg,
		fullManager:   full,
		manager:       full,
		mcpHost:       host,
		workspaceRoot: workspace,
	}
}

func TestMCPServerStatesNil(t *testing.T) {
	var r *Runtime
	if got := r.MCPServerStates(); got != nil {
		t.Errorf("nil runtime states = %v, want nil", got)
	}
	if _, err := r.MCPTestServer("x"); err == nil {
		t.Error("nil runtime test accepted")
	}
	if err := r.MCPAddServer("x", "y", nil); err == nil {
		t.Error("nil runtime add accepted")
	}
	if err := r.MCPRemoveServer("x"); err == nil {
		t.Error("nil runtime remove accepted")
	}
	if err := r.MCPSetServerEnabled("x", true); err == nil {
		t.Error("nil runtime enable accepted")
	}
}

func TestMCPServerStatesLive(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	host := startMCPHostForTest(t, cfg, full)
	rt := fakeRuntimeWithMCP(t, cfg, full, host, t.TempDir())
	states := rt.MCPServerStates()
	if len(states) != 1 {
		t.Fatalf("states = %+v, want one server", states)
	}
	st := states[0]
	if st.Name != "demo" || !st.Enabled || st.State != "ready" {
		t.Errorf("state = %+v, want ready demo", st)
	}
	if len(st.Tools) != 1 || st.Tools[0] != "mcp__demo__echo" {
		t.Errorf("tools = %v", st.Tools)
	}
	if st.Command == "" || st.TimeoutSeconds != 30 {
		t.Errorf("config echo = %+v", st)
	}
}

func TestMCPServerStatesFailedAndDisabled(t *testing.T) {
	full := mcpTestFullManager(t)
	off := false
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"bad": mcpServerConfig(t, map[string]string{"FF_MCP_BAD_VERSION": "1"}),
		"off": func() mcp.ServerConfig {
			sc := mcpServerConfig(t, nil)
			sc.Enabled = &off
			sc.Command = "/nonexistent-binary-xyz"
			return sc
		}(),
	})
	host := startMCPHostForTest(t, cfg, full)
	rt := fakeRuntimeWithMCP(t, cfg, full, host, t.TempDir())
	byName := map[string]MCPServerState{}
	for _, st := range rt.MCPServerStates() {
		byName[st.Name] = st
	}
	bad, ok := byName["bad"]
	if !ok || bad.State != "failed" || bad.Error == "" {
		t.Errorf("bad = %+v, want failed with reason", bad)
	}
	offSt, ok := byName["off"]
	if !ok || offSt.State != "disabled" || offSt.Enabled {
		t.Errorf("off = %+v, want disabled", offSt)
	}
	// Disabled bogus command must never have spawned: no tools, no error.
	if len(offSt.Tools) != 0 {
		t.Errorf("disabled tools = %v", offSt.Tools)
	}
}

func TestMCPServerStatesPersistedUnknown(t *testing.T) {
	full := mcpTestFullManager(t)
	ws := t.TempDir()
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	host, err := startMCPHost(cfg, ws, full)
	if err != nil {
		t.Fatalf("startMCPHost: %v", err)
	}
	if err := persistMCPStatus(host, ws, cfg); err != nil {
		t.Fatalf("persistMCPStatus: %v", err)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("host Close: %v", err)
	}
	// Fresh runtime with no live Host: last-known tools, unknown state.
	rt := fakeRuntimeWithMCP(t, cfg, full, nil, ws)
	states := rt.MCPServerStates()
	if len(states) != 1 {
		t.Fatalf("states = %+v", states)
	}
	st := states[0]
	if st.State != "unknown" {
		t.Errorf("state = %q, want unknown (never claim reachability)", st.State)
	}
	if len(st.Tools) != 1 || st.Tools[0] != "mcp__demo__echo" {
		t.Errorf("last-known tools = %v", st.Tools)
	}
}

func TestMCPAddServer(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(nil)
	rt := fakeRuntimeWithMCP(t, cfg, full, nil, t.TempDir())
	if err := rt.MCPAddServer("demo", "/usr/local/bin/demo", []string{"--stdio"}); err != nil {
		t.Fatalf("MCPAddServer: %v", err)
	}
	// In-memory swap (SetModel precedent) plus real persistence: reload.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load after add: %v", err)
	}
	got, err := reloaded.GetMCPServer("demo")
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	if got.Command != "/usr/local/bin/demo" || len(got.Args) != 1 || !got.IsEnabled() {
		t.Errorf("stored = %+v", got)
	}
	// Mutating the caller's slice must not alias stored config.
	if err := rt.MCPAddServer("demo", "/bin/x", nil); err == nil {
		t.Error("duplicate add accepted")
	}
	if err := rt.MCPAddServer("bad name!", "/bin/x", nil); err == nil {
		t.Error("invalid key accepted")
	}
	if err := rt.MCPAddServer("nocmd", "", nil); err == nil {
		t.Error("empty command accepted")
	}
	// Failed adds leave no partial entry (verify on reload: the
	// methods swap the runtime config pointer, SetModel-style).
	reloaded2, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := reloaded2.GetMCPServer("nocmd"); err == nil {
		t.Error("failed add left partial entry")
	}
}

func TestMCPRemoveServer(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"a": mcpServerConfig(t, nil),
		"b": mcpServerConfig(t, nil),
	})
	rt := fakeRuntimeWithMCP(t, cfg, full, nil, t.TempDir())
	if err := rt.MCPRemoveServer("nope"); err == nil {
		t.Error("removing unknown server accepted")
	}
	if err := rt.MCPRemoveServer("a"); err != nil {
		t.Fatalf("MCPRemoveServer: %v", err)
	}
	// Verify on reload: methods swap the runtime config pointer.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := reloaded.GetMCPServer("a"); err == nil {
		t.Error("removed server still present")
	}
	if _, err := reloaded.GetMCPServer("b"); err != nil {
		t.Errorf("unrelated server disturbed: %v", err)
	}
}

func TestMCPSetServerEnabled(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, nil),
	})
	rt := fakeRuntimeWithMCP(t, cfg, full, nil, t.TempDir())
	if err := rt.MCPSetServerEnabled("nope", false); err == nil {
		t.Error("unknown server accepted")
	}
	if err := rt.MCPSetServerEnabled("demo", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	demo, _ := got.GetMCPServer("demo")
	if demo.IsEnabled() || demo.Command == "" {
		t.Errorf("after disable: %+v (fields must survive)", demo)
	}
	if err := rt.MCPSetServerEnabled("demo", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got, err = config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	demo, _ = got.GetMCPServer("demo")
	if !demo.IsEnabled() {
		t.Error("not re-enabled")
	}
}

func TestMCPTestServer(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
		"bad":  mcpServerConfig(t, map[string]string{"FF_MCP_BAD_VERSION": "1"}),
	})
	off := false
	badCmd := mcpServerConfig(t, nil)
	badCmd.Enabled = &off
	badCmd.Command = "/nonexistent-binary-xyz"
	cfg.MCP.Servers["off"] = badCmd
	rt := fakeRuntimeWithMCP(t, cfg, full, nil, t.TempDir())

	res, err := rt.MCPTestServer("demo")
	if err != nil {
		t.Fatalf("MCPTestServer: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0] != "mcp__demo__echo" {
		t.Errorf("tools = %v", res.Tools)
	}
	if _, err := rt.MCPTestServer("nope"); err == nil {
		t.Error("unknown server accepted")
	}
	if _, err := rt.MCPTestServer("off"); err == nil {
		t.Error("disabled server test accepted")
	}
	if _, err := rt.MCPTestServer("bad"); err == nil {
		t.Error("failing server test accepted")
	}
}

func TestMCPTestServerCleansUp(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	rt := fakeRuntimeWithMCP(t, cfg, full, nil, t.TempDir())
	before := len(full.Definitions())
	for i := 0; i < 3; i++ {
		if _, err := rt.MCPTestServer("demo"); err != nil {
			t.Fatalf("MCPTestServer: %v", err)
		}
	}
	// Ephemeral test hosts must not leak into the frozen universe.
	if got := len(full.Definitions()); got != before {
		t.Errorf("manager grew %d -> %d across test runs", before, got)
	}
}

func TestMCPServerStatesNilConfig(t *testing.T) {
	rt := &Runtime{}
	if got := rt.MCPServerStates(); got != nil {
		t.Errorf("states = %v, want nil without config", got)
	}
}
