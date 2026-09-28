package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/mcp"
)

// MCP status persistence at runtime boundaries (Phase 7). The runtime
// writes the Host snapshot for doctor after startup and before teardown;
// failures warn via MCPWarnings and never fail the run.

func TestMCPStatusPersistedOnStartup(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	root := t.TempDir()
	host, err := startMCPHost(cfg, root, full)
	if err != nil {
		t.Fatalf("startMCPHost: %v", err)
	}
	defer func() { _ = host.Close() }()
	if err := persistMCPStatus(host, root, cfg); err != nil {
		t.Fatalf("persistMCPStatus: %v", err)
	}
	st, err := mcp.ReadStatusFile(root)
	if err != nil {
		t.Fatalf("ReadStatusFile: %v", err)
	}
	if !st.CurrentFor(cfg.MCP) {
		t.Error("freshly persisted status must be current")
	}
	if len(st.Servers) != 1 || st.Servers[0].Key != "demo" || !st.Servers[0].Healthy {
		t.Errorf("servers = %+v, want one healthy demo", st.Servers)
	}
	if len(st.Servers[0].Tools) != 1 || st.Servers[0].Tools[0] != "mcp__demo__echo" {
		t.Errorf("tools = %v, want [mcp__demo__echo]", st.Servers[0].Tools)
	}
}

func TestMCPStatusPersistNilHost(t *testing.T) {
	if err := persistMCPStatus(nil, t.TempDir(), mcpTestConfig(nil)); err != nil {
		t.Errorf("nil host must write nothing and succeed, got %v", err)
	}
}

func TestMCPStatusWriteFailureWarns(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	root := t.TempDir()
	host, err := startMCPHost(cfg, root, full)
	if err != nil {
		t.Fatalf("startMCPHost: %v", err)
	}
	defer func() { _ = host.Close() }()
	// A file where the workspace root should be makes persistence fail;
	// the runtime must proceed regardless and surface the note.
	blocker, err := os.CreateTemp("", "mcp-status-blocker-*")
	if err != nil {
		t.Fatal(err)
	}
	blockerPath := blocker.Name()
	blocker.Close()
	defer os.Remove(blockerPath)
	err = persistMCPStatus(host, blockerPath, cfg)
	if err == nil {
		t.Fatal("expected persistence failure through a file path")
	}
	rt := &Runtime{
		cfg:           cfg,
		fullManager:   full,
		manager:       full,
		mcpHost:       host,
		mcpStatusNote: "mcp status not persisted: test-note",
		agents:        agent.DefaultRegistry(),
		activeAgent:   "general",
	}
	found := false
	for _, w := range rt.MCPWarnings() {
		if strings.Contains(w, "test-note") {
			found = true
		}
	}
	if !found {
		t.Errorf("MCPWarnings = %v, want the persistence note", rt.MCPWarnings())
	}
	// Natives and the live adapter are unaffected by the failed write.
	if _, ok := full.Lookup("mcp__demo__echo"); !ok {
		t.Error("adapter missing after status write failure")
	}
}

func TestMCPClosePersistsFinalState(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	root := t.TempDir()
	host, err := startMCPHost(cfg, root, full)
	if err != nil {
		t.Fatalf("startMCPHost: %v", err)
	}
	rt := &Runtime{
		cfg:           cfg,
		fullManager:   full,
		manager:       full,
		mcpHost:       host,
		agents:        agent.DefaultRegistry(),
		activeAgent:   "general",
		workspaceRoot: root,
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st, err := mcp.ReadStatusFile(root)
	if err != nil {
		t.Fatalf("status missing after Close: %v", err)
	}
	if len(st.Servers) != 1 || st.Servers[0].Key != "demo" {
		t.Errorf("servers = %+v", st.Servers)
	}
	// Second Close stays safe and does not require the host.
	if err := rt.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestMCPCloseWithoutStatusDir(t *testing.T) {
	// Close on a vanished workspace must still shut the host down: the
	// persist attempt fails silently and bounded shutdown wins.
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	root := t.TempDir()
	host, err := startMCPHost(cfg, root, full)
	if err != nil {
		t.Fatalf("startMCPHost: %v", err)
	}
	rt := &Runtime{
		cfg:           cfg,
		fullManager:   full,
		manager:       full,
		mcpHost:       host,
		agents:        agent.DefaultRegistry(),
		activeAgent:   "general",
		workspaceRoot: filepath.Join(root, "gone"),
	}
	if err := rt.Close(); err != nil {
		t.Errorf("Close with missing workspace: %v", err)
	}
}

func TestMCPStatusHonorsToolsRegistry(t *testing.T) {
	// Status content derives from the Host snapshot, independent of which
	// agents opted in: registration filtering must not leak into it.
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	root := t.TempDir()
	host, err := startMCPHost(cfg, root, full)
	if err != nil {
		t.Fatalf("startMCPHost: %v", err)
	}
	defer func() { _ = host.Close() }()
	filtered, err := full.Filtered([]string{"read_file"})
	if err != nil {
		t.Fatalf("Filtered: %v", err)
	}
	_ = filtered
	st := mcp.NewStatusFile(host.Snapshot(), cfg.MCP, 1)
	if len(st.Servers) != 1 || len(st.Servers[0].Tools) != 1 {
		t.Errorf("status must reflect discovery, not agent filtering: %+v", st.Servers)
	}
}
