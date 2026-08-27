package permissions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func isolateHomeForConfigstore(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestConfigStore_RoundTripsRules(t *testing.T) {
	home := isolateHomeForConfigstore(t)

	// Ensure config exists with defaults.
	if _, err := config.Load(); err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	store := NewConfigStore()

	// Save a set of rules with multiple tools and non-default default.
	original := Rules{
		Default: Allow,
		Tools: map[string]Decision{
			"read_file":  Allow,
			"write_file": Deny,
			"shell":      Ask,
		},
	}
	if err := store.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Default != original.Default {
		t.Errorf("Default = %v, want %v", loaded.Default, original.Default)
	}
	for k, want := range original.Tools {
		if got := loaded.Tools[k]; got != want {
			t.Errorf("Tools[%q] = %v, want %v", k, got, want)
		}
	}
	if len(loaded.Tools) != len(original.Tools) {
		t.Errorf("Tools len = %d, want %d", len(loaded.Tools), len(original.Tools))
	}

	// Verify the underlying config.yaml actually persisted the values
	// and that the file is isolated to the temp home.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load after Save: %v", err)
	}
	if cfg.Permissions.Default != original.Default.String() {
		t.Errorf("config.Permissions.Default = %q, want %q", cfg.Permissions.Default, original.Default.String())
	}
	for k, v := range original.Tools {
		if got := cfg.Permissions.Tools[k]; got != v.String() {
			t.Errorf("config.Permissions.Tools[%q] = %q, want %q", k, got, v.String())
		}
	}
	// Ensure we didn't touch real home.
	if _, err := os.Stat(filepath.Join(home, ".forcefield", "config.yaml")); err != nil {
		t.Fatalf("expected config in temp home: %v", err)
	}
}

func TestConfigStore_EmptyAndDefaultRules(t *testing.T) {
	isolateHomeForConfigstore(t)
	if _, err := config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	store := NewConfigStore()

	// Empty tools with Ask default (zero value is Allow=0, but config default is "ask").
	// An empty Rules should round-trip as Ask default via the config layer's empty->Ask handling.
	empty := Rules{
		Default: Ask,
		Tools:   map[string]Decision{},
	}
	if err := store.Save(empty); err != nil {
		t.Fatalf("Save empty: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if loaded.Default != Ask {
		t.Errorf("empty Default = %v, want Ask", loaded.Default)
	}
	if len(loaded.Tools) != 0 {
		t.Errorf("empty Tools len = %d, want 0", len(loaded.Tools))
	}

	// Default Allow with no tools.
	allow := Rules{Default: Allow, Tools: map[string]Decision{}}
	if err := store.Save(allow); err != nil {
		t.Fatalf("Save allow: %v", err)
	}
	loaded, err = store.Load()
	if err != nil {
		t.Fatalf("Load allow: %v", err)
	}
	if loaded.Default != Allow {
		t.Errorf("Allow Default = %v, want Allow", loaded.Default)
	}
}

func TestConfigStore_MultipleRulesPreserved(t *testing.T) {
	isolateHomeForConfigstore(t)
	if _, err := config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	store := NewConfigStore()
	rules := Rules{
		Default: Deny,
		Tools: map[string]Decision{
			"read_file":          Allow,
			"write_file":         Allow,
			"shell":              Deny,
			"list_files":         Ask,
			"add_project_memory": Deny,
		},
	}
	if err := store.Save(rules); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Default != Deny {
		t.Errorf("Default = %v, want Deny", loaded.Default)
	}
	for k, want := range rules.Tools {
		if got := loaded.Tools[k]; got != want {
			t.Errorf("Tools[%q] = %v, want %v", k, got, want)
		}
	}
	// Ensure Manager integration still works via ConfigStore.
	m, err := NewManager(store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if got := m.Check("shell"); got != Deny {
		t.Errorf("Check shell = %v, want Deny", got)
	}
	if got := m.Check("unknown_tool"); got != Deny {
		t.Errorf("Check unknown = %v, want Deny (default)", got)
	}
}

func TestConfigStore_InvalidPersistedData(t *testing.T) {
	home := isolateHomeForConfigstore(t)
	if _, err := config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Write an invalid permissions section directly via config file.
	path, err := config.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	raw := "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: ornith:9b\npermissions:\n  default: sometimes\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store := NewConfigStore()
	_, err = store.Load()
	if err == nil {
		t.Fatal("Load with invalid permissions.default should error")
	}
	if !strings.Contains(err.Error(), "permissions.default") {
		t.Errorf("error %q should mention permissions.default", err)
	}

	// Also test invalid per-tool value.
	raw2 := "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: ornith:9b\npermissions:\n  default: ask\n  tools:\n    shell: sometimes\n"
	if err := os.WriteFile(path, []byte(raw2), 0o600); err != nil {
		t.Fatalf("WriteFile2: %v", err)
	}
	_, err = store.Load()
	if err == nil {
		t.Fatal("Load with invalid tool decision should error")
	}
	if !strings.Contains(err.Error(), "permissions.tools.shell") {
		t.Errorf("error %q should mention permissions.tools.shell", err)
	}

	// Corrupted YAML should also error and not affect home isolation.
	if err := os.WriteFile(path, []byte("not: [yaml"), 0o600); err != nil {
		t.Fatalf("WriteFile corrupt: %v", err)
	}
	_, err = store.Load()
	if err == nil {
		t.Fatal("Load with corrupted YAML should error")
	}
	// Ensure temp home still isolated.
	if _, statErr := os.Stat(filepath.Join(home, ".forcefield", "config.yaml")); statErr != nil {
		t.Fatalf("config still in temp home: %v", statErr)
	}
}

func TestConfigStore_SavePreservesOtherConfigSections(t *testing.T) {
	isolateHomeForConfigstore(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	origProvider := cfg.Model.Provider
	origName := cfg.Model.Name

	store := NewConfigStore()
	rules := Rules{Default: Allow, Tools: map[string]Decision{"shell": Deny}}
	if err := store.Save(rules); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if reloaded.Model.Provider != origProvider || reloaded.Model.Name != origName {
		t.Errorf("model section changed: got %q/%q, want %q/%q", reloaded.Model.Provider, reloaded.Model.Name, origProvider, origName)
	}
	if reloaded.Permissions.Default != "allow" {
		t.Errorf("Permissions.Default = %q, want allow", reloaded.Permissions.Default)
	}
	if got := reloaded.Permissions.Tools["shell"]; got != "deny" {
		t.Errorf("Permissions.Tools[shell] = %q, want deny", got)
	}
}

func TestRulesFromConfig_AndConfigFromRules(t *testing.T) {
	// Direct unit test for helpers to cover error paths and round-trip.
	cases := []struct {
		rules Rules
	}{
		{Rules{Default: Allow, Tools: map[string]Decision{"a": Allow}}},
		{Rules{Default: Deny, Tools: map[string]Decision{"b": Deny, "c": Ask}}},
		{Rules{Default: Ask, Tools: map[string]Decision{}}},
	}
	for _, tc := range cases {
		p := configFromRules(tc.rules)
		got, err := rulesFromConfig(p)
		if err != nil {
			t.Fatalf("rulesFromConfig error: %v", err)
		}
		if got.Default != tc.rules.Default {
			t.Errorf("Default = %v, want %v", got.Default, tc.rules.Default)
		}
		for k, want := range tc.rules.Tools {
			if got.Tools[k] != want {
				t.Errorf("Tools[%q] = %v, want %v", k, got.Tools[k], want)
			}
		}
	}
	// Empty string should be treated as Ask.
	p := config.Permissions{Default: "", Tools: map[string]string{"shell": ""}}
	got, err := rulesFromConfig(p)
	if err != nil {
		t.Fatalf("empty permissions: %v", err)
	}
	if got.Default != Ask {
		t.Errorf("empty Default = %v, want Ask", got.Default)
	}
	if got.Tools["shell"] != Ask {
		t.Errorf("empty tool decision = %v, want Ask", got.Tools["shell"])
	}
}
