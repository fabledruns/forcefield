package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func breakSaveDestination(t *testing.T, s *Session) string {
	t.Helper()
	path := filepath.Join(".forcefield", "sessions", s.ID+".json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSaveFailurePreservesTurnAndSupervisor(t *testing.T) {
	_ = chdirTemp(t)
	s := New()
	s.AddMessage("user", "goal")
	if err := s.Save(); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	s.BeginTurn()
	s.AddPendingCall(toolCall("c-life", "shell"))
	s.Supervisor = &SupervisorState{Restarts: 2}

	path := breakSaveDestination(t, s)
	if err := s.Save(); err == nil {
		t.Fatal("expected Save to fail when destination is a directory")
	}
	// In-memory lifecycle is preserved for retry (snapshot covers the
	// pre-save state, which includes the new turn + supervisor).
	if s.Turn == nil || len(s.Turn.Pending) != 1 || s.Turn.Pending[0].ID != "c-life" {
		t.Errorf("Turn = %+v, want the running batch preserved", s.Turn)
	}
	if s.Supervisor == nil || s.Supervisor.Restarts != 2 {
		t.Errorf("Supervisor = %+v, want Restarts=2 preserved", s.Supervisor)
	}
	if s.LastSaveError == "" {
		t.Error("LastSaveError empty after failed save, want diagnostic text")
	}
	if s.LastSaveTime.IsZero() {
		t.Error("LastSaveTime zero after failed save, want a timestamp")
	}
	// The file itself is untouched (still a directory, no torn file).
	if info, _ := os.Stat(path); !info.IsDir() {
		t.Error("destination should still be a directory after failed save")
	}
	// Retry converges after cleanup.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("save after cleanup: %v", err)
	}
	if s.LastSaveError != "" {
		t.Errorf("LastSaveError = %q after success, want cleared", s.LastSaveError)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Turn == nil || len(loaded.Turn.Pending) != 1 {
		t.Errorf("reloaded Turn = %+v, want the batch", loaded.Turn)
	}
	if loaded.Supervisor == nil || loaded.Supervisor.Restarts != 2 {
		t.Errorf("reloaded Supervisor = %+v, want Restarts=2", loaded.Supervisor)
	}
}

func TestSaveFailureErrorMentionsRenameAttempts(t *testing.T) {
	_ = chdirTemp(t)
	s := New()
	s.AddMessage("user", "hi")
	if err := s.Save(); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	path := breakSaveDestination(t, s)
	defer func() { _ = os.RemoveAll(path) }()
	err := s.Save()
	if err == nil {
		t.Fatal("expected Save to fail")
	}
	if !strings.Contains(err.Error(), "attempts") {
		t.Errorf("save error = %q, want rename-attempt diagnostics", err.Error())
	}
	if !strings.Contains(s.LastSaveError, "attempts") {
		t.Errorf("LastSaveError = %q, want the same diagnostics", s.LastSaveError)
	}
}

func TestRenameAttemptsBounded(t *testing.T) {
	if renameAttempts < 8 || renameAttempts > 20 {
		t.Errorf("renameAttempts = %d, want a bounded window tolerating AV holds (8-20)", renameAttempts)
	}
}
