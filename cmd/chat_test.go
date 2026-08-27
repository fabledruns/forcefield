package cmd

import (
	"fmt"
	"testing"

	"forcefield/internal/session"
)

func TestChatCommand_UsesInjectedStarter(t *testing.T) {
	origStarter := chatStarter
	origNew := newChatSession
	defer func() {
		chatStarter = origStarter
		newChatSession = origNew
	}()

	called := false
	var gotSess *session.Session
	fakeSess := session.New()
	newChatSession = func() *session.Session { return fakeSess }
	chatStarter = func(s *session.Session) error {
		called = true
		gotSess = s
		if s != fakeSess {
			t.Errorf("session mismatch")
		}
		return nil
	}

	// Invoke the RunE directly (Args validation is NoArgs, so empty is fine).
	if err := chatCmd.Args(chatCmd, []string{}); err != nil {
		t.Fatalf("Args validation: %v", err)
	}
	fn := chatCmd.RunE
	if fn == nil {
		t.Fatal("chatCmd RunE is nil")
	}
	if err := fn(chatCmd, []string{}); err != nil {
		t.Fatalf("chatStarter error = %v", err)
	}
	if !called {
		t.Error("chatStarter was not called")
	}
	if gotSess == nil {
		t.Error("session was nil")
	}
}

func TestChatCommand_PropagatesError(t *testing.T) {
	origStarter := chatStarter
	origNew := newChatSession
	defer func() {
		chatStarter = origStarter
		newChatSession = origNew
	}()

	newChatSession = session.New
	chatStarter = func(*session.Session) error {
		return fmt.Errorf("tui failure")
	}
	err := chatCmd.RunE(chatCmd, []string{})
	if err == nil || err.Error() != "tui failure" {
		t.Errorf("expected tui failure, got %v", err)
	}
}

func TestRootCommand_StartsChatViaInjection(t *testing.T) {
	origRootStarter := rootTuiStarter
	origNewSession := newSession
	origLoadSession := loadSession
	defer func() {
		rootTuiStarter = origRootStarter
		newSession = origNewSession
		loadSession = origLoadSession
	}()

	called := false
	rootTuiStarter = func(s *session.Session) error {
		called = true
		if s == nil {
			t.Error("session nil")
		}
		return nil
	}
	newSession = func() *session.Session { return session.New() }
	loadSession = func(id string) (*session.Session, error) {
		t.Fatalf("loadSession should not be called for empty resumeID, got %q", id)
		return nil, nil
	}
	resumeID = ""
	if err := rootCmd.RunE(rootCmd, []string{}); err != nil {
		t.Fatalf("root RunE error = %v", err)
	}
	if !called {
		t.Error("rootTuiStarter not called")
	}
}

func TestRootCommand_ResumeLoadsSession(t *testing.T) {
	origRootStarter := rootTuiStarter
	origLoadSession := loadSession
	origNewSession := newSession
	defer func() {
		rootTuiStarter = origRootStarter
		loadSession = origLoadSession
		newSession = origNewSession
	}()

	// Use a fake session to avoid filesystem.
	fake := session.New()
	loadSession = func(id string) (*session.Session, error) {
		if id != "my-id" {
			t.Errorf("loadSession id = %q, want my-id", id)
		}
		return fake, nil
	}
	newSession = func() *session.Session {
		t.Error("newSession should not be called when resumeID set")
		return nil
	}
	called := false
	rootTuiStarter = func(s *session.Session) error {
		called = true
		if s != fake {
			t.Error("session mismatch on resume")
		}
		return nil
	}
	resumeID = "my-id"
	t.Cleanup(func() { resumeID = "" })
	if err := rootCmd.RunE(rootCmd, []string{}); err != nil {
		t.Fatalf("root RunE resume error = %v", err)
	}
	if !called {
		t.Error("rootTuiStarter not called on resume")
	}
}
