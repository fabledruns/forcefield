package cmd

import (
	"os"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/session"
)

func TestRootCommand_AgentFlagOverridesSession(t *testing.T) {
	// Hermetic working dir: RunE persists the session via Save().
	dir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(origWd)

	origStarter := rootTuiStarter
	origNew := newSession
	defer func() {
		rootTuiStarter = origStarter
		newSession = origNew
		agentFlag = ""
	}()

	agentFlag = "coding"
	fake := session.New()
	newSession = func() *session.Session { return fake }
	var got *session.Session
	rootTuiStarter = func(s *session.Session) error {
		got = s
		return nil
	}
	resumeID = ""
	if err := rootCmd.RunE(rootCmd, []string{}); err != nil {
		t.Fatalf("root RunE: %v", err)
	}
	if got == nil || got.Agent != "coding" {
		t.Fatalf("session agent = %v, want coding", got)
	}
}

func TestChatCommand_AgentFlagSetsSession(t *testing.T) {
	// Hermetic working dir: RunE persists the session via Save().
	dir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(origWd)

	origStarter := chatStarter
	origNew := newChatSession
	defer func() {
		chatStarter = origStarter
		newChatSession = origNew
		agentFlag = ""
	}()

	agentFlag = "cyber"
	// Use a temp dir so Save() does not touch the real workspace.
	fake := session.New()
	newChatSession = func() *session.Session { return fake }
	var got *session.Session
	chatStarter = func(s *session.Session) error {
		got = s
		return nil
	}
	if err := chatCmd.RunE(chatCmd, []string{}); err != nil {
		t.Fatalf("chat RunE: %v", err)
	}
	if got == nil || got.Agent != "cyber" {
		t.Fatalf("session agent = %v, want cyber", got)
	}
}

func TestRunCommand_AgentFlagUnknownAgentErrors(t *testing.T) {
	defer func() { agentFlag = "" }()
	agentFlag = "nonexistent-agent"
	// runtimeNew would build a real runtime (config load); the agent error
	// must surface before any model call. Use a fake runtimeNew to avoid
	// touching the real config: point at the real constructor but expect
	// the SetAgent error path. Instead, verify the flag branch rejects
	// unknown agents without calling runtimeRun.
	origRun := runtimeRun
	defer func() { runtimeRun = origRun }()
	called := false
	runtimeRun = func([]providers.Message) (providers.Response, error) {
		called = true
		return providers.Response{Content: "ok"}, nil
	}
	err := runCommand([]string{"hello"})
	// With an unknown agent the command must fail (either via real
	// runtimeNew/SetAgent or before reaching runtimeRun).
	if err == nil {
		t.Fatalf("expected error for unknown agent")
	}
	if called {
		t.Fatalf("runtimeRun should not be called when --agent is set")
	}
}
