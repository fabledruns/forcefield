package config

import (
	"strings"
	"testing"
)

const mcpTestModel = "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"

// loadMCP writes body after the minimal model block and loads it, so MCP
// tests share one valid envelope and differ only in their mcp:/tools:
// sections.
func loadMCP(t *testing.T, body string) (*Config, error) {
	t.Helper()
	isolateHome(t)
	writeConfig(t, mcpTestModel+body)
	return Load()
}

func TestLoadAcceptsValidMCPServer(t *testing.T) {
	cfg, err := loadMCP(t, "mcp:\n  servers:\n    filesystem:\n      command: /usr/local/bin/mcp-filesystem\n      args: [/data]\n      timeout_seconds: 30\n")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	srv := cfg.MCP.Servers["filesystem"]
	if srv.Command != "/usr/local/bin/mcp-filesystem" {
		t.Errorf("command = %q", srv.Command)
	}
	if !srv.IsEnabled() {
		t.Error("server must default to enabled")
	}
}

func TestLoadAcceptsAbsentMCPBlock(t *testing.T) {
	cfg, err := loadMCP(t, "")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.MCP.Servers) != 0 {
		t.Errorf("servers = %v, want empty (existing configs unchanged)", cfg.MCP.Servers)
	}
}

func TestLoadAcceptsDisabledServerWithoutCommand(t *testing.T) {
	cfg, err := loadMCP(t, "mcp:\n  servers:\n    future:\n      enabled: false\n")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MCP.Servers["future"].IsEnabled() {
		t.Error("server must be disabled")
	}
}

func TestLoadRejectsBadServerName(t *testing.T) {
	_, err := loadMCP(t, "mcp:\n  servers:\n    \"has space\":\n      command: /bin/srv\n")
	if err == nil {
		t.Fatal("Load() accepted an invalid server name")
	}
	if !strings.Contains(err.Error(), "has space") {
		t.Errorf("error %q does not name the offending server", err)
	}
}

func TestLoadRejectsMissingCommandWhenEnabled(t *testing.T) {
	_, err := loadMCP(t, "mcp:\n  servers:\n    srv:\n      args: [/x]\n")
	if err == nil {
		t.Fatal("Load() accepted an enabled server without a command")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("error %q does not point at the missing command", err)
	}
}

func TestLoadRejectsShellCommand(t *testing.T) {
	_, err := loadMCP(t, "mcp:\n  servers:\n    srv:\n      command: \"run; rm -rf /\"\n")
	if err == nil {
		t.Fatal("Load() accepted a shell metacharacter in command")
	}
}

func TestLoadRejectsOversizedArgs(t *testing.T) {
	big := strings.Repeat("a", 5000)
	_, err := loadMCP(t, "mcp:\n  servers:\n    srv:\n      command: /bin/srv\n      args: ["+big+"]\n")
	if err == nil {
		t.Fatal("Load() accepted an oversized arg")
	}
}

func TestLoadRejectsOversizedEnv(t *testing.T) {
	big := strings.Repeat("v", 9000)
	_, err := loadMCP(t, "mcp:\n  servers:\n    srv:\n      command: /bin/srv\n      env:\n        OK: "+big+"\n")
	if err == nil {
		t.Fatal("Load() accepted an oversized env value")
	}
}

func TestLoadRejectsBadTimeout(t *testing.T) {
	_, err := loadMCP(t, "mcp:\n  servers:\n    srv:\n      command: /bin/srv\n      timeout_seconds: 999\n")
	if err == nil {
		t.Fatal("Load() accepted a timeout above the 300s ceiling")
	}
}

func TestLoadRejectsBadPassthrough(t *testing.T) {
	_, err := loadMCP(t, "mcp:\n  servers:\n    srv:\n      command: /bin/srv\n      env_passthrough: [9LIVES]\n")
	if err == nil {
		t.Fatal("Load() accepted an invalid passthrough name")
	}
}

func TestLoadRejectsTooManyServers(t *testing.T) {
	var b strings.Builder
	b.WriteString("mcp:\n  servers:\n")
	for i := 0; i < 9; i++ {
		b.WriteString("    srv")
		b.WriteString(string(rune('a' + i)))
		b.WriteString(":\n      command: /bin/srv\n")
	}
	if _, err := loadMCP(t, b.String()); err == nil {
		t.Fatal("Load() accepted more than the maximum enabled servers")
	}
}

func TestLoadAcceptsMCPToolLimitOverride(t *testing.T) {
	cfg, err := loadMCP(t, "tools:\n  mcp__filesystem__read_file:\n    timeout_seconds: 60\n")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Tools["mcp__filesystem__read_file"].TimeoutSeconds != 60 {
		t.Errorf("override = %+v, want 60s timeout", cfg.Tools["mcp__filesystem__read_file"])
	}
}

func TestLoadStillRejectsUnknownNativeTool(t *testing.T) {
	_, err := loadMCP(t, "tools:\n  frobnicate:\n    timeout_seconds: 10\n")
	if err == nil {
		t.Fatal("Load() accepted an unknown native tool name")
	}
}

func TestLoadRoundTripsMCPBlock(t *testing.T) {
	cfg, err := loadMCP(t, "mcp:\n  servers:\n    srv:\n      command: /bin/srv\n      timeout_seconds: 45\n")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	reloaded, err := Load()
	if err != nil {
		t.Fatalf("re-Load() error = %v", err)
	}
	if reloaded.MCP.Servers["srv"].Command != "/bin/srv" {
		t.Errorf("round trip command = %q", reloaded.MCP.Servers["srv"].Command)
	}
}
