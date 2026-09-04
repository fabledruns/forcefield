package runtime

import (
	"strings"
	"testing"
)

func writeLegacyAgentConfig(t *testing.T, agentSection, agentsSection string) {
	t.Helper()
	body := "model:\n  provider: ollama\n  name: ornith:9b\n\nproviders:\n  ollama:\n    type: ollama\n\n" + agentSection
	if agentsSection != "" {
		body += "\n" + agentsSection
	}
	body += "\npermissions:\n  default: ask\n"
	writeConfigFile(t, body)
}

func TestLegacyCustomAgentNamePreservedAsDisplay(t *testing.T) {
	isolateRuntimeHome(t)
	writeLegacyAgentConfig(t, "agent:\n  name: Jarvis\n  system_prompt: \"You are Jarvis, a helpful harness agent.\"\n", "")

	cfg := loadTestConfig(t)
	rt, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	// Behaviour key stays general; the custom label is display-only.
	if got := rt.CurrentAgent(); got != "general" {
		t.Fatalf("CurrentAgent = %q, want general", got)
	}
	if got := rt.AgentDisplayName(); got != "Jarvis" {
		t.Fatalf("AgentDisplayName = %q, want Jarvis", got)
	}
	if prompt := rt.agent.BuildSystemPrompt(); !strings.Contains(prompt, "You are Jarvis") {
		t.Fatalf("legacy system_prompt not applied, prompt starts:\n%.200s", prompt)
	}
	// Legacy prompt wins over the built-in general identity.
	if prompt := rt.agent.BuildSystemPrompt(); strings.Contains(prompt, "local-first agent harness for running specialised") {
		t.Fatalf("built-in general prompt should not appear when legacy prompt set")
	}
}

func TestLegacyDefaultNameUsesGeneral(t *testing.T) {
	isolateRuntimeHome(t)
	writeLegacyAgentConfig(t, "agent:\n  name: default\n", "")

	cfg := loadTestConfig(t)
	rt, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if got := rt.CurrentAgent(); got != "general" {
		t.Fatalf("CurrentAgent = %q, want general", got)
	}
	if got := rt.AgentDisplayName(); got != "general" {
		t.Fatalf("AgentDisplayName = %q, want general", got)
	}
}

func TestLegacyKnownAgentNameSelectsIt(t *testing.T) {
	isolateRuntimeHome(t)
	writeLegacyAgentConfig(t, "agent:\n  name: legal\n", "")

	cfg := loadTestConfig(t)
	rt, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if got := rt.CurrentAgent(); got != "legal" {
		t.Fatalf("CurrentAgent = %q, want legal", got)
	}
	// Legal must not expose shell.
	for _, d := range rt.manager.Definitions() {
		if d.Name == "shell" {
			t.Fatalf("legal agent should not expose shell")
		}
	}
}

func TestAgentOverridePromptBeatsLegacyPrompt(t *testing.T) {
	isolateRuntimeHome(t)
	writeLegacyAgentConfig(t,
		"agent:\n  name: general\n  system_prompt: \"legacy prompt\"\n",
		"agents:\n  general:\n    system_prompt: \"override prompt\"\n")

	cfg := loadTestConfig(t)
	rt, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	prompt := rt.agent.BuildSystemPrompt()
	if !strings.Contains(prompt, "override prompt") {
		t.Fatalf("agents.general override should win, prompt starts:\n%.200s", prompt)
	}
	if strings.Contains(prompt, "legacy prompt") {
		t.Fatalf("legacy prompt should lose to explicit override")
	}
}

func TestLegacyPromptAppliesToNonGeneralAgent(t *testing.T) {
	isolateRuntimeHome(t)
	writeLegacyAgentConfig(t, "agent:\n  name: docs\n  system_prompt: \"legacy docs prompt\"\n", "")

	cfg := loadTestConfig(t)
	rt, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if got := rt.CurrentAgent(); got != "docs" {
		t.Fatalf("CurrentAgent = %q, want docs", got)
	}
	if prompt := rt.agent.BuildSystemPrompt(); !strings.Contains(prompt, "legacy docs prompt") {
		t.Fatalf("legacy prompt should apply, prompt starts:\n%.200s", prompt)
	}
}
