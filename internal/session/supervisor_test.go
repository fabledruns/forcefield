package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Clean sessions serialize exactly as before: no supervisor block.
func TestSupervisorStateOmittedWhenClean(t *testing.T) {
	_ = chdirTemp(t)
	s := New()
	s.AddMessage("user", "hello")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(".forcefield", "sessions", s.ID+".json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "supervisor") {
		t.Errorf("clean session file contains supervisor state:\n%s", raw)
	}
	if s.Supervisor != nil {
		t.Errorf("new session Supervisor = %+v, want nil", s.Supervisor)
	}
}

// Lifecycle state round-trips through save/load untouched.
func TestSupervisorStateRoundTrip(t *testing.T) {
	_ = chdirTemp(t)
	s := New()
	s.AddMessage("user", "hello")
	s.Supervisor = &SupervisorState{Restarts: 3, ExhaustedAt: time.Now().UTC().Unix()}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Supervisor == nil {
		t.Fatal("Supervisor did not survive round-trip")
	}
	if loaded.Supervisor.Restarts != 3 || loaded.Supervisor.ExhaustedAt != s.Supervisor.ExhaustedAt {
		t.Errorf("Supervisor = %+v, want %+v", loaded.Supervisor, s.Supervisor)
	}
	// Conversation and turn envelope are unaffected by the block.
	if len(loaded.Messages) != 1 || loaded.Messages[0].Content != "hello" {
		t.Errorf("Messages = %+v, want the single user message", loaded.Messages)
	}
}

// Old files without the block load as clean (nil Supervisor).
func TestSupervisorStateAbsentLoadsNil(t *testing.T) {
	dir := chdirTemp(t)
	s := New()
	s.AddMessage("user", "hello")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, ".forcefield", "sessions", s.ID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "supervisor") {
		t.Fatalf("fixture unexpectedly contains supervisor state")
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Supervisor != nil {
		t.Errorf("Supervisor = %+v, want nil for old files", loaded.Supervisor)
	}
}
