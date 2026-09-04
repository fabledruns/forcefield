package config

import (
	"os"
	"path/filepath"
	"testing"
)

// helper to write temp config and load via validation (not via home dir)
func writeTempConfig(t *testing.T, content string) *Config {
	t.Helper()
	dir := t.TempDir()
	origHome, _ := os.UserHomeDir()
	// We test validate directly, not Load, to avoid home dir mocking.
	// Instead parse yaml and call validate.
	var cfg Config
	_ = dir
	_ = origHome
	_ = content
	// Use yaml unmarshal path: we call validate after manual struct.
	return &cfg
}

func TestValidateAgents_UnknownAgentRejected(t *testing.T) {
	cfg := Config{
		Model: Model{Provider: "ollama", Name: "test"},
		Agents: map[string]AgentConfig{
			"unknown": {Description: "x"},
		},
	}
	if err := validateAgents(cfg.Agents); err == nil {
		t.Fatalf("expected error for unknown agent")
	}
}

func TestValidateAgents_DuplicateToolRejected(t *testing.T) {
	cfg := Config{
		Model: Model{Provider: "ollama", Name: "test"},
		Agents: map[string]AgentConfig{
			"coding": {Tools: []string{"read_file", "read_file"}},
		},
	}
	if err := validateAgents(cfg.Agents); err == nil {
		t.Fatalf("expected duplicate tool error")
	}
}

func TestValidateAgents_EmptyToolRejected(t *testing.T) {
	cfg := Config{
		Model: Model{Provider: "ollama", Name: "test"},
		Agents: map[string]AgentConfig{
			"coding": {Tools: []string{""}},
		},
	}
	if err := validateAgents(cfg.Agents); err == nil {
		t.Fatalf("expected empty tool error")
	}
}

func TestValidateAgents_Valid(t *testing.T) {
	cfg := Config{
		Model: Model{Provider: "ollama", Name: "test"},
		Agents: map[string]AgentConfig{
			"coding": {Tools: []string{"read_file", "shell"}},
			"legal":  {SystemPrompt: "custom prompt"},
		},
	}
	if err := validateAgents(cfg.Agents); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfig_LoadWithAgents(t *testing.T) {
	dir := t.TempDir()
	// Mock home by setting HOME env
	t.Setenv("HOME", dir)
	if os.Getenv("OS") != "" {
		// Windows uses USERPROFILE
		t.Setenv("USERPROFILE", dir)
	}
	forcefieldHome := filepath.Join(dir, ".forcefield")
	_ = os.MkdirAll(forcefieldHome, 0o700)
	cfgPath := filepath.Join(forcefieldHome, "config.yaml")
	content := `model:
  provider: ollama
  name: test-model
agent:
  name: general
agents:
  coding:
    tools:
      - read_file
      - shell
  legal:
    system_prompt: "custom legal prompt"
permissions:
  default: ask
sandbox:
  mode: native
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with agents: %v", err)
	}
	if cfg.Agents["coding"].Tools[0] != "read_file" {
		t.Fatalf("coding tools not loaded")
	}
	if cfg.Agents["legal"].SystemPrompt != "custom legal prompt" {
		t.Fatalf("legal prompt not loaded")
	}
}

func TestConfig_RejectsInvalidAgentsInFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if os.Getenv("OS") != "" {
		t.Setenv("USERPROFILE", dir)
	}
	forcefieldHome := filepath.Join(dir, ".forcefield")
	_ = os.MkdirAll(forcefieldHome, 0o700)
	cfgPath := filepath.Join(forcefieldHome, "config.yaml")
	content := `model:
  provider: ollama
  name: test-model
agents:
  unknown:
    tools: [read_file]
permissions:
  default: ask
sandbox:
  mode: native
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(); err == nil {
		t.Fatalf("expected Load to fail for unknown agent")
	}
}
