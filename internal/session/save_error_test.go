package session

import (
	"strings"
	"testing"
)

// TestSaveInvalidIDRecordsLastSaveError pins that every Save failure is
// observable through LastSaveError — including validation rejections,
// which previously returned early without recording anything, leaving
// the headless save-health gate and /status blind to them.
func TestSaveInvalidIDRecordsLastSaveError(t *testing.T) {
	s := New()
	s.ID = "bad/id"
	if err := s.Save(); err == nil {
		t.Fatal("Save() with an invalid ID succeeded, want an error")
	}
	if s.LastSaveError == "" {
		t.Fatal("LastSaveError empty after failed save, want diagnostic text")
	}
	if !strings.Contains(s.LastSaveError, "invalid session id") {
		t.Errorf("LastSaveError = %q, want the validation diagnostics", s.LastSaveError)
	}
	if s.LastSaveTime.IsZero() {
		t.Error("LastSaveTime is zero after failed save, want it recorded")
	}
}
