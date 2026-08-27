package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSessionSaveRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	s := New()
	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	sessDir := filepath.Join(dir, filepath.FromSlash(sessionsDir))
	info, err := os.Stat(sessDir)
	if err != nil {
		t.Fatalf("Stat sessions dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("sessions dir perm = %04o, want 0700", perm)
	}
	path := filepath.Join(sessDir, s.ID+".json")
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("Stat session file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("session file perm = %04o, want 0600", perm)
	}
}

func TestSessionSavePreservesExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	s := New()
	if err := s.Save(); err != nil {
		t.Fatalf("Save() first: %v", err)
	}
	path := filepath.Join(dir, filepath.FromSlash(sessionsDir), s.ID+".json")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod 0600: %v", err)
	}
	// Modify and save again — should preserve 0600.
	s.AddMessage("user", "hello again")
	if err := s.Save(); err != nil {
		t.Fatalf("Save() second: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("preserved perm = %04o, want 0600", perm)
	}
	// Also preserve a 0644 if it exists (do not fail).
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod 0644: %v", err)
	}
	s.AddMessage("user", "third")
	if err := s.Save(); err != nil {
		t.Fatalf("Save() third: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("Stat third: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("preserved 0644 perm = %04o, want 0644", perm)
	}
}

func TestSessionDirectoryChmodRepairsPermissive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	s1 := New()
	if err := s1.Save(); err != nil {
		t.Fatalf("Save s1: %v", err)
	}
	sessDir := filepath.Join(dir, filepath.FromSlash(sessionsDir))
	if err := os.Chmod(sessDir, 0o755); err != nil {
		t.Fatalf("Chmod 0755: %v", err)
	}
	s2 := New()
	if err := s2.Save(); err != nil {
		t.Fatalf("Save s2: %v", err)
	}
	info, err := os.Stat(sessDir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("repaired dir perm = %04o, want 0700", perm)
	}
}
