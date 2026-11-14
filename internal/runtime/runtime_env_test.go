package runtime

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/sandbox"
	"forcefield/internal/task"
)

func TestRuntimeSystemPromptIncludesEnvironmentMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	ws := t.TempDir()
	cfg := &config.Config{}
	cfg.Model.Provider = "openai"
	cfg.Model.Name = "gpt-4o-mini"
	cfg.Workspace.Root = ws
	cfg.Workspace.Mode = config.WorkspaceStrict
	cfg.Sandbox.Mode = string(sandbox.ModeNative)

	rt, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	defer func() { _ = rt.Close() }()

	canonicalWS, err := filepath.EvalSymlinks(ws)
	if err != nil {
		canonicalWS = ws
	}

	prompt := rt.agent.BuildSystemPrompt()
	expectedHost := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	for _, want := range []string{
		"## Runtime Environment",
		expectedHost,
		canonicalWS,
		"Workspace mode: strict",
		"Sandbox mode: native",
		"process working directory",
		"confined mode",
		`"."`,
		"/home/agent/repo",
		`"root"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing %q:\n%s", want, prompt)
		}
	}

	msgs := rt.buildMessages([]providers.Message{{Role: providers.UserRole, Content: "inspect repo"}})
	if len(msgs) == 0 || msgs[0].Role != providers.SystemRole {
		t.Fatalf("expected leading system message, got %#v", msgs)
	}
	if !strings.Contains(msgs[0].Content, canonicalWS) || !strings.Contains(msgs[0].Content, "## Runtime Environment") {
		t.Fatalf("buildMessages dropped runtime environment metadata:\n%s", msgs[0].Content)
	}
}

func TestRuntimeEnvironmentMetadataSurvivesContextRebuilding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	ws := t.TempDir()
	cfg := &config.Config{}
	cfg.Model.Provider = "openai"
	cfg.Model.Name = "gpt-4o-mini"
	cfg.Workspace.Root = ws
	cfg.Sandbox.Mode = string(sandbox.ModeNative)

	rt, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	defer func() { _ = rt.Close() }()

	canonicalWS, err := filepath.EvalSymlinks(ws)
	if err != nil {
		canonicalWS = ws
	}

	snap := rt.snapshotRunState()
	snap.mode = ModePlan
	snap.contextBudget = ContextBudget{
		Limit:       2500,
		Reserve:     300,
		MaxMessages: 4,
	}

	// 1. Build initial prompt and verify buildPromptBase + refreshSystemPrompt preserve metadata.
	history := []providers.Message{{Role: providers.UserRole, Content: "goal: trace working directory"}}
	for i := 0; i < 12; i++ {
		history = append(history,
			providers.Message{
				Role:    providers.AssistantRole,
				Content: strings.Repeat("thinking ", 40),
				ToolCalls: []providers.ToolCall{{
					ID:   fmt.Sprintf("call-%d", i),
					Name: "list_files",
				}},
			},
			providers.Message{
				Role:       providers.ToolRole,
				ToolCallID: fmt.Sprintf("call-%d", i),
				Name:       "list_files",
				Content:    strings.Repeat("entry\n", 40),
			},
		)
	}

	messages := buildMessagesWithBudget(history, snap.agent, snap.contextBudget)
	promptBase, ok := buildPromptBase(snap)
	if !ok {
		t.Fatal("buildPromptBase returned ok=false")
	}

	state := task.New("trace working directory")
	state.Apply(task.Patch{CurrentStep: "Inspect sandbox policy"})
	refreshSystemPrompt(messages, promptBase, ok, state)

	// 2. Rebuild provider window under tight eviction budget.
	windowed := windowForProvider(messages, snap)
	if len(windowed) == 0 || windowed[0].Role != providers.SystemRole {
		t.Fatalf("expected system message after windowForProvider, got %#v", windowed)
	}
	for _, want := range []string{
		"## Runtime Environment",
		canonicalWS,
		"Workspace mode: permissive",
		"Sandbox mode: native",
		"process working directory",
		"confined mode",
		"/home/agent/repo",
		"## Current Task State",
	} {
		if !strings.Contains(windowed[0].Content, want) {
			t.Errorf("windowed system prompt missing %q:\n%s", want, windowed[0].Content)
		}
	}

	// 3. Also verify fallback path when leading system message is absent.
	rebuilt := windowForProvider(history, snap)
	if len(rebuilt) == 0 || rebuilt[0].Role != providers.SystemRole {
		t.Fatalf("expected rebuilt system message, got %#v", rebuilt)
	}
	if !strings.Contains(rebuilt[0].Content, canonicalWS) || !strings.Contains(rebuilt[0].Content, "## Runtime Environment") {
		t.Fatalf("rebuilt prompt missing runtime metadata:\n%s", rebuilt[0].Content)
	}
}

func TestRuntimeEnvironmentFormattingWindowsVsUnix(t *testing.T) {
	win := formatRuntimeEnvironment("windows", "amd64", `C:\Users\Admin\Documents\projects\forcefield`, sandbox.ModeNative)
	for _, want := range []string{
		"windows/amd64",
		`C:\Users\Admin\Documents\projects\forcefield`,
		"Sandbox mode: native",
		"Windows host paths",
		"inside WSL",
		`"."`,
		"/home/agent/repo",
		`"root"`,
	} {
		if !strings.Contains(win, want) {
			t.Errorf("windows env text missing %q:\n%s", want, win)
		}
	}

	unix := formatRuntimeEnvironment("linux", "amd64", "/workspace/forcefield", sandbox.ModeIsolated)
	if strings.Contains(unix, "inside WSL") {
		t.Errorf("unix env text should not mention WSL:\n%s", unix)
	}
	for _, want := range []string{
		"/workspace/forcefield",
		"Sandbox mode: isolated",
		"process working directory",
		"confined mode",
	} {
		if !strings.Contains(unix, want) {
			t.Errorf("unix env text missing %q:\n%s", want, unix)
		}
	}
}
