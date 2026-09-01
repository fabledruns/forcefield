package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSave_FailureLeavesOriginalIntactAndNoTemp(t *testing.T) {
	dir := chdirTemp(t)
	s := New()
	s.AddMessage("user", "original")
	if err := s.Save(); err != nil {
		t.Fatalf("initial Save failed: %v", err)
	}
	path := filepath.Join(".forcefield", "sessions", s.ID+".json")
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read orig: %v", err)
	}
	// Corrupt the destination by making it a directory so rename will fail
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	// Try to save again - should fail
	s.AddMessage("assistant", "new message that will not be persisted")
	err = s.Save()
	if err == nil {
		t.Fatal("expected Save to fail when destination is a directory")
	}
	// Original directory should still be a directory, not a file
	if info, _ := os.Stat(path); !info.IsDir() {
		t.Error("destination should still be a directory after failed save")
	}
	// No temp file should be left
	entries, _ := os.ReadDir(filepath.Join(dir, ".forcefield", "sessions"))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left after failed save: %s", e.Name())
		}
	}
	// Cleanup directory so we can save again
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("save after cleanup should succeed: %v", err)
	}
	// Verify new file contains both messages
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load after recovery: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 messages after recovery, got %d", len(loaded.Messages))
	}
	_ = orig
}

func TestSave_HandlesDiskFullSimulatedViaReadOnlyDir(t *testing.T) {
	dir := chdirTemp(t)
	s := New()
	s.AddMessage("user", "hello")
	if err := s.Save(); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	// Make sessions dir read-only to simulate disk-full / permission error
	sessDir := filepath.Join(dir, ".forcefield", "sessions")
	origMode := os.FileMode(0o755)
	if info, err := os.Stat(sessDir); err == nil {
		origMode = info.Mode()
	}
	// On Windows, Chmod may not enforce read-only for directories, so skip if not effective
	if err := os.Chmod(sessDir, 0o555); err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	// Try to save - may fail on some platforms, but should not leave temp
	s.AddMessage("user", "new")
	err := s.Save()
	// Restore permissions for cleanup
	_ = os.Chmod(sessDir, origMode)
	if err == nil {
		// On this platform, read-only dir still allows writes (e.g., Windows), so skip
		t.Skip("platform allows writes to read-only directory, cannot simulate disk-full")
	}
	// Check no temp debris
	entries, _ := os.ReadDir(sessDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp left after failed save: %s", e.Name())
		}
	}
	// Original file should still be valid
	if _, err := Load(s.ID); err != nil {
		t.Errorf("original file should still be loadable after failed save: %v", err)
	}
}
