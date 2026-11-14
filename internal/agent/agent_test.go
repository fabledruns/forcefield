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

func TestBuildSystemPromptIncludesRuntimeEnvAndWindowsGuidance(t *testing.T) {
	winEnv := FormatEnvironment(EnvironmentInfo{
		OS:            "windows",
		Arch:          "amd64",
		WorkspaceRoot: `C:\projects\forcefield`,
		SandboxMode:   "native",
	})
	a := New("default", "identity", "").WithRuntimeEnv(winEnv)
	got := a.BuildSystemPrompt()

	for _, want := range []string{
		"## Runtime Environment",
		"windows/amd64",
		`C:\projects\forcefield`,
		"Sandbox mode: native",
		"Windows host paths",
		"inside WSL",
		"Filesystem tools path usage",
		"omit `path` or use `\".\"` to target the workspace root",
		"permissive native mode",
		"process working directory",
		"actual `Workspace root` host path",
		"confined mode",
		"workspace.mode: strict",
		"sandbox.mode: wsl",
		"sandbox.mode: isolated",
		`"."`,
		"/home/agent/repo",
		`"root"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Windows runtime prompt missing %q:\n%s", want, got)
		}
	}

	linuxEnv := FormatEnvironment(EnvironmentInfo{
		OS:            "linux",
		Arch:          "amd64",
		WorkspaceRoot: "/tmp/forcefield",
		SandboxMode:   "isolated",
	})
	linuxPrompt := New("default", "identity", "").WithRuntimeEnv(linuxEnv).BuildSystemPrompt()
	for _, want := range []string{
		"## Runtime Environment",
		"linux/amd64",
		"/tmp/forcefield",
		"Sandbox mode: isolated",
		"/home/agent/repo",
	} {
		if !strings.Contains(linuxPrompt, want) {
			t.Errorf("Linux runtime prompt missing %q:\n%s", want, linuxPrompt)
		}
	}
	if strings.Contains(linuxEnv, "inside WSL") || strings.Contains(linuxPrompt, "Windows / WSL boundary") {
		t.Errorf("Linux runtime prompt should not include Windows/WSL note:\n%s", linuxPrompt)
	}
}

func TestFormatEnvironmentDistinguishesPermissiveAndConfinedModes(t *testing.T) {
	if got := FormatEnvironment(EnvironmentInfo{}); got != "" {
		t.Fatalf("FormatEnvironment(empty) = %q, want empty", got)
	}

	permissive := FormatEnvironment(EnvironmentInfo{
		OS:            "linux",
		Arch:          "amd64",
		WorkspaceRoot: "/repo/forcefield",
		WorkspaceMode: "permissive",
		SandboxMode:   "native",
	})
	for _, want := range []string{
		"Workspace mode: permissive",
		"Sandbox mode: native",
		"for filesystem tools, omit `path` or use `\".\"` to target the workspace root",
		"omitting `cwd` uses the process working directory",
		"`\".\"` resolves relative to the process working directory",
		"pass the actual `Workspace root` host path",
		"omitting `cwd` or using `\".\"` targets the workspace root",
	} {
		if !strings.Contains(permissive, want) {
			t.Errorf("permissive prompt missing %q:\n%s", want, permissive)
		}
	}
	if strings.Contains(permissive, "omit `path` / `cwd`") {
		t.Errorf("prompt still conflates filesystem path and shell cwd:\n%s", permissive)
	}

	strict := FormatEnvironment(EnvironmentInfo{
		OS:            "windows",
		Arch:          "amd64",
		WorkspaceRoot: `C:\Users\Admin\Documents\projects\forcefield`,
		WorkspaceMode: "strict",
		SandboxMode:   "native",
	})
	if !strings.Contains(strict, "Workspace mode: strict") {
		t.Errorf("strict prompt missing Workspace mode: strict:\n%s", strict)
	}
}
