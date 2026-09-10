package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome points the Forcefield home directory at a fresh temp dir
// for the duration of the test, so tests never touch a real ~/.forcefield.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("HOME", home)        // os.UserHomeDir everywhere else
	return home
}

func TestLoadCreatesValidDefaultOnFirstRun(t *testing.T) {
	isolateHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Model.Provider == "" || cfg.Model.Endpoint == "" || cfg.Model.Name == "" {
		t.Fatalf("default config missing model fields: %+v", cfg.Model)
	}
	if cfg.Permissions.Default != "ask" && cfg.Permissions.Default != "" {
		t.Errorf("default permissions = %q, want ask or empty", cfg.Permissions.Default)
	}

	// The generated file must itself be loadable: a default that fails its
	// own validation would brick first-run entirely.
	if _, err := os.Stat(mustPath(t)); err != nil {
		t.Fatalf("default config file not created: %v", err)
	}
	if _, err := Load(); err != nil {
		t.Fatalf("re-Load() of created default error = %v", err)
	}
}

func mustPath(t *testing.T) string {
	t.Helper()
	p, err := Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	return p
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := mustPath(t)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	isolateHome(t)
	path := writeConfig(t, "model: [unclosed")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() on malformed YAML returned no error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not mention the config path", err)
	}
}

func TestLoadRejectsMissingRequiredFields(t *testing.T) {
	for name, body := range map[string]string{
		"provider": "model:\n  endpoint: http://localhost:11434\n  name: m\n",
		"name":     "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n",
	} {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeConfig(t, body)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() accepted config missing model.%s", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name the missing field %q", err, name)
			}
		})
	}
}

// TestLoadDefaultsEndpointFromCatalog pins that known providers get their
// default base URL from the built-in catalog, so a cloud provider can be
// selected without repeating its endpoint.
func TestLoadDefaultsEndpointFromCatalog(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: openai\n  name: gpt-4o-mini\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("openai", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider(openai) error = %v", err)
	}
	if resolved.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("base URL = %q, want the OpenAI catalog default", resolved.BaseURL)
	}
}

// TestLoadRejectsCustomProviderWithoutAnyEndpoint makes sure a provider
// with no catalog default still fails loudly when no endpoint is given.
func TestLoadRejectsCustomProviderWithoutAnyEndpoint(t *testing.T) {
	isolateHome(t)
	writeConfig(t,
		"model:\n  provider: local-llm\n  name: m\n"+
			"providers:\n  local-llm:\n    type: openai-compatible\n")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a custom provider with no base_url")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Errorf("error %q does not point at the missing base_url", err)
	}
}

// TestLoadContextBudgetFields pins that the additive context-budget keys
// parse and that omitting them keeps zero values (existing configs keep
// working unchanged with table/default resolution).
func TestLoadContextBudgetFields(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"+
		"agent:\n  name: default\n  context_window: 16000\n  context_reserve: 1000\n"+
		"  max_context_messages: 42\n  context_summary: true\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Agent.ContextWindow != 16000 {
		t.Errorf("ContextWindow = %d, want 16000", cfg.Agent.ContextWindow)
	}
	if cfg.Agent.ContextReserve != 1000 {
		t.Errorf("ContextReserve = %d, want 1000", cfg.Agent.ContextReserve)
	}
	if cfg.Agent.MaxContextMessages != 42 {
		t.Errorf("MaxContextMessages = %d, want 42", cfg.Agent.MaxContextMessages)
	}
	if !cfg.Agent.ContextSummary {
		t.Error("ContextSummary = false, want true")
	}
}

func TestLoadContextBudgetFieldsDefaultToZero(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Agent.ContextWindow != 0 || cfg.Agent.ContextReserve != 0 ||
		cfg.Agent.MaxContextMessages != 0 || cfg.Agent.ContextSummary {
		t.Errorf("context fields = %+v, want zeros (backwards compatible)", cfg.Agent)
	}
}

// TestLoadToolsOverrides pins that the additive tools: block parses and
// that omitting it keeps a nil map (existing configs behave identically).
func TestLoadToolsOverrides(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"+
		"tools:\n  shell:\n    max_bytes: 1048576\n    timeout_seconds: 60\n  search_files:\n    max_lines: 40\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Tools["shell"].MaxBytes != 1048576 {
		t.Errorf("shell max_bytes = %d, want 1048576", cfg.Tools["shell"].MaxBytes)
	}
	if cfg.Tools["shell"].TimeoutSeconds != 60 {
		t.Errorf("shell timeout = %v, want 60", cfg.Tools["shell"].TimeoutSeconds)
	}
	if cfg.Tools["search_files"].MaxLines != 40 {
		t.Errorf("search max_lines = %d, want 40", cfg.Tools["search_files"].MaxLines)
	}
}

func TestLoadToolsDefaultsToNil(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Tools) != 0 {
		t.Errorf("tools = %+v, want empty (backwards compatible)", cfg.Tools)
	}
}

func TestLoadRejectsInvalidToolsOverrides(t *testing.T) {
	for name, body := range map[string]string{
		"unknown tool":         "tools:\n  frobnicate:\n    max_bytes: 10\n",
		"negative bytes":       "tools:\n  shell:\n    max_bytes: -5\n",
		"negative lines":       "tools:\n  shell:\n    max_lines: -1\n",
		"timeout over ceiling": "tools:\n  shell:\n    timeout_seconds: 301\n",
	} {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"+body)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted invalid tools override (%s)", name)
			}
		})
	}
}

// TestLoadWorkspaceDefaults pins backwards compatibility: configs
// without a workspace block load with empty root and permissive mode.
func TestLoadWorkspaceDefaults(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Workspace.Root != "" || (cfg.Workspace.Mode != "" && cfg.Workspace.Mode != WorkspacePermissive) {
		t.Errorf("workspace = %+v, want empty (permissive default)", cfg.Workspace)
	}
	if mode, err := ParseWorkspaceMode(cfg.Workspace.Mode); err != nil || mode != WorkspacePermissive {
		t.Errorf("ParseWorkspaceMode(%q) = %q, %v; want permissive", cfg.Workspace.Mode, mode, err)
	}
}

func TestLoadWorkspaceStrict(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"+
		"workspace:\n  root: /tmp\n  mode: strict\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Workspace.Root != "/tmp" || cfg.Workspace.Mode != WorkspaceStrict {
		t.Errorf("workspace = %+v", cfg.Workspace)
	}
}

func TestLoadRejectsBadWorkspaceMode(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"+
		"workspace:\n  mode: fortress\n")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted unknown workspace mode")
	} else if !strings.Contains(err.Error(), "workspace.mode") {
		t.Errorf("error = %v, want it to name workspace.mode", err)
	}
}

// TestLoadAgentRuntimeSettings pins per-agent run bounds: explicit
// values parse, omitted stays nil/zero, and explicit false is distinct
// from unset for the summary flag.
func TestLoadAgentRuntimeSettings(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"+
		"agents:\n  coding:\n    max_iterations: 20\n    max_tool_calls: 50\n"+
		"    context_window: 16000\n    context_summary: true\n"+
		"  legal:\n    context_summary: false\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	coding := cfg.Agents["coding"]
	if coding.MaxIterations != 20 || coding.MaxToolCalls != 50 || coding.ContextWindow != 16000 {
		t.Errorf("coding = %+v", coding)
	}
	if coding.ContextSummary == nil || !*coding.ContextSummary {
		t.Errorf("coding summary = %v, want explicit true", coding.ContextSummary)
	}
	legal := cfg.Agents["legal"]
	if legal.ContextSummary == nil || *legal.ContextSummary {
		t.Errorf("legal summary = %v, want explicit false (distinct from unset)", legal.ContextSummary)
	}
	if cfg.Agents["general"].ContextSummary != nil {
		t.Error("unset summary must stay nil")
	}
}

func TestLoadRejectsInvalidPermissionValues(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://x\n  name: m\npermissions:\n  default: sometimes\n")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted invalid permissions.default")
	}
	if !strings.Contains(err.Error(), "permissions.default") {
		t.Errorf("error %q does not name permissions.default", err)
	}
	if !strings.Contains(err.Error(), "sometimes") {
		t.Errorf("error %q does not quote the invalid value", err)
	}
}

func TestSaveRoundTripsAndNeverWritesAPIKey(t *testing.T) {
	isolateHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg.Permissions.Tools = map[string]string{"shell": "allow"}
	cfg.Model.APIKey = "super-secret-key"

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	raw, err := os.ReadFile(mustPath(t))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(raw), "super-secret-key") {
		t.Fatal("API key was written to disk")
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatalf("re-Load() error = %v", err)
	}
	if reloaded.Permissions.Tools["shell"] != "allow" {
		t.Errorf("tools.shell = %q after round-trip, want allow", reloaded.Permissions.Tools["shell"])
	}
}

func TestSaveIsAtomicAndLeavesNoDebris(t *testing.T) {
	isolateHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for i := 0; i < 3; i++ {
		cfg.Model.Name = "model-" + strings.Repeat("x", i+1)
		if err := cfg.Save(); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	dir := filepath.Dir(mustPath(t))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read home dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	final, err := Load()
	if err != nil {
		t.Fatalf("final Load() error = %v", err)
	}
	if final.Model.Name != cfg.Model.Name {
		t.Errorf("final model = %q, want %q", final.Model.Name, cfg.Model.Name)
	}
}

func TestValidatePermissionValues(t *testing.T) {
	for _, valid := range []string{"", "allow", "deny", "ask"} {
		if err := validatePermissionValue("f", valid); err != nil {
			t.Errorf("validatePermissionValue(%q) = %v, want nil", valid, err)
		}
	}
	if err := validatePermissionValue("f", "maybe"); err == nil {
		t.Error("validatePermissionValue(\"maybe\") = nil, want error")
	}
}

// TestDefaultRuntimeToolsResolveToAsk pins the N16 contract: the shipped
// default config has no explicit allow entry for the runtime-registered
// load_skill/update_task_state tools, so they resolve through
// permissions.default ("ask") and stay fail-closed. cue/tools.cue
// documents the same effective default; all three sides must agree.
func TestDefaultRuntimeToolsResolveToAsk(t *testing.T) {
	isolateHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Permissions.Default != "ask" {
		t.Fatalf("default permissions = %q, want ask (the fail-closed fallback)", cfg.Permissions.Default)
	}
	for _, tool := range []string{"load_skill", "update_task_state"} {
		if got, ok := cfg.Permissions.Tools[tool]; ok && got == "allow" {
			t.Errorf("default permissions.tools.%s = allow, want unset (ask via default)", tool)
		}
	}
}

func TestLoadRejectsNegativeAgentLimits(t *testing.T) {
	for name, body := range map[string]string{
		"global max_iterations":     "agent:\n  max_iterations: -5\n",
		"global max_tool_calls":     "agent:\n  max_tool_calls: -1\n",
		"global max_failures":       "agent:\n  max_consecutive_failures: -2\n",
		"global context_window":     "agent:\n  context_window: -100\n",
		"global context_reserve":    "agent:\n  context_reserve: -10\n",
		"global context_messages":   "agent:\n  max_context_messages: -3\n",
		"per-agent max_iterations":  "agents:\n  coding:\n    max_iterations: -5\n",
		"per-agent context_window":  "agents:\n  coding:\n    context_window: -100\n",
		"per-agent max_tool_calls":  "agents:\n  legal:\n    max_tool_calls: -1\n",
		"per-agent context_reserve": "agents:\n  docs:\n    context_reserve: -4\n",
	} {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"+body)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted negative limit (%s): silently defaulting would hide a typo", name)
			}
		})
	}
}

func TestLoadAcceptsZeroAgentLimits(t *testing.T) {
	// Zero keeps its documented meaning (fall back to the default) and
	// must load; only negatives are rejected.
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n"+
		"agent:\n  max_iterations: 0\n  max_tool_calls: 0\n  context_window: 0\n"+
		"agents:\n  coding:\n    max_iterations: 0\n    context_window: 0\n")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() rejected zero limits, want them valid: %v", err)
	}
}
