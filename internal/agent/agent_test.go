package agent

import (
	"strings"
	"testing"
)

func TestBuildSystemPromptAlwaysIncludesContract(t *testing.T) {
	a := New("default", "You are Forcefield, a local-first coding agent.", "")
	got := a.BuildSystemPrompt()

	if !strings.HasPrefix(got, "You are Forcefield, a local-first coding agent.") {
		t.Fatalf("expected base identity at the start of the prompt, got:\n%s", got)
	}
	if !strings.Contains(got, "## Operating contract") {
		t.Fatalf("expected the operating contract to be appended, got:\n%s", got)
	}
	if strings.Contains(got, "## Skill Catalog") {
		t.Fatalf("did not expect a skill catalog section when the catalog is empty, got:\n%s", got)
	}
	if strings.Contains(got, "## Project Memory") {
		t.Fatalf("did not expect a project memory section when none was set, got:\n%s", got)
	}
}

func TestBuildSystemPromptAppendsSkillCatalog(t *testing.T) {
	a := New("default", "identity", "- id: `debug`, name: \"Debug\" — find root causes")
	got := a.BuildSystemPrompt()

	if !strings.Contains(got, "## Skill Catalog") {
		t.Fatalf("expected a skill catalog section, got:\n%s", got)
	}
	if !strings.Contains(got, "id: `debug`") {
		t.Fatalf("expected catalog entries to appear, got:\n%s", got)
	}
	if !strings.Contains(got, "load_skill") {
		t.Fatalf("expected load_skill instructions in the catalog section, got:\n%s", got)
	}
}

func TestBuildSystemPromptAppendsProjectMemory(t *testing.T) {
	a := New("default", "identity", "").
		WithProjectMemory("- uses go 1.24")
	got := a.BuildSystemPrompt()

	if !strings.Contains(got, "## Project Memory") {
		t.Fatalf("expected a project memory section, got:\n%s", got)
	}
	if !strings.Contains(got, "uses go 1.24") {
		t.Fatalf("expected remembered facts to appear, got:\n%s", got)
	}
}

func TestOperatingContractContainsBehavioralAnchors(t *testing.T) {
	// These phrases are the mechanisms the contract is supposed to install.
	// If they disappear, the agent silently loses a failure-mode defense.
	required := []string{
		"smallest change that works",
		"Examples are not requirements",
		"Keep one environment model",
		"may not share a filesystem",
		"compare the required syntax and semantics",
		"inspect the harness, evaluation order",
		"Handle X gracefully",
		"Do not rerun unchanged checks",
		"Set verification to passed only if you ran the check",
		"Do not expose hidden chain-of-thought",
		"cheapest sufficient tool",
		"Do not stop merely because the task requires several tool calls",
		"no longer producing useful information",
	}

	for _, phrase := range required {
		if !strings.Contains(agentContract, phrase) {
			t.Errorf("operating contract missing behavioral anchor %q", phrase)
		}
	}
}
