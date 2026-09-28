package config

import (
	"testing"

	"forcefield/internal/mcp"
)

func validMCPServerConfig() mcp.ServerConfig {
	return mcp.ServerConfig{Command: "/usr/local/bin/demo-server", Args: []string{"--stdio"}}
}

func TestAddMCPServer(t *testing.T) {
	c := &Config{}
	sc := validMCPServerConfig()
	if err := c.AddMCPServer("demo", sc); err != nil {
		t.Fatalf("AddMCPServer: %v", err)
	}
	got, err := c.GetMCPServer("demo")
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	if got.Command != sc.Command {
		t.Errorf("stored command = %q, want %q", got.Command, sc.Command)
	}
	if err := c.AddMCPServer("demo", sc); err == nil {
		t.Error("duplicate AddMCPServer accepted")
	}
	if err := c.AddMCPServer("bad name!", sc); err == nil {
		t.Error("invalid key accepted")
	}
	bad := validMCPServerConfig()
	bad.Command = ""
	if err := c.AddMCPServer("nocmd", bad); err == nil {
		t.Error("commandless enabled server accepted")
	}
	if _, err := c.GetMCPServer("nocmd"); err == nil {
		t.Error("failed add left a partial entry")
	}
}

func TestRemoveMCPServer(t *testing.T) {
	c := &Config{}
	if err := c.AddMCPServer("a", validMCPServerConfig()); err != nil {
		t.Fatalf("AddMCPServer: %v", err)
	}
	if err := c.AddMCPServer("b", validMCPServerConfig()); err != nil {
		t.Fatalf("AddMCPServer: %v", err)
	}
	if err := c.RemoveMCPServer("nope"); err == nil {
		t.Error("removing unknown server accepted")
	}
	if err := c.RemoveMCPServer("a"); err != nil {
		t.Fatalf("RemoveMCPServer: %v", err)
	}
	if _, err := c.GetMCPServer("a"); err == nil {
		t.Error("removed server still present")
	}
	if _, err := c.GetMCPServer("b"); err != nil {
		t.Errorf("unrelated server disturbed: %v", err)
	}
	var nilCfg *Config
	if err := nilCfg.RemoveMCPServer("a"); err == nil {
		t.Error("nil config accepted")
	}
	if err := nilCfg.AddMCPServer("a", validMCPServerConfig()); err == nil {
		t.Error("nil config accepted")
	}
	if _, err := nilCfg.GetMCPServer("a"); err == nil {
		t.Error("nil config accepted")
	}
}

func TestSetMCPServerEnabled(t *testing.T) {
	c := &Config{}
	if err := c.AddMCPServer("demo", validMCPServerConfig()); err != nil {
		t.Fatalf("AddMCPServer: %v", err)
	}
	if err := c.SetMCPServerEnabled("nope", false); err == nil {
		t.Error("unknown server accepted")
	}
	if err := c.SetMCPServerEnabled("demo", false); err != nil {
		t.Fatalf("SetMCPServerEnabled: %v", err)
	}
	got, err := c.GetMCPServer("demo")
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	if got.IsEnabled() {
		t.Error("server still enabled after disable")
	}
	if got.Command == "" {
		t.Error("disable destroyed unrelated fields")
	}
	if err := c.SetMCPServerEnabled("demo", true); err != nil {
		t.Fatalf("SetMCPServerEnabled: %v", err)
	}
	got, _ = c.GetMCPServer("demo")
	if !got.IsEnabled() {
		t.Error("server not re-enabled")
	}
}

func TestSetMCPServerEnabledBudgetReverts(t *testing.T) {
	c := &Config{}
	// Fill the enabled budget, keep one disabled spare.
	for i := 0; i < 8; i++ {
		if err := c.AddMCPServer("srv"+string(rune('a'+i)), validMCPServerConfig()); err != nil {
			t.Fatalf("AddMCPServer: %v", err)
		}
	}
	off := false
	extra := validMCPServerConfig()
	extra.Enabled = &off
	if err := c.AddMCPServer("spare", extra); err != nil {
		t.Fatalf("AddMCPServer disabled: %v", err)
	}
	if err := c.SetMCPServerEnabled("spare", true); err == nil {
		t.Fatal("enabling past the 8-server budget accepted")
	}
	got, err := c.GetMCPServer("spare")
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	if got.IsEnabled() {
		t.Error("failed enable left the server enabled")
	}
}
