package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDirCreatesWithRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	home := isolateHome(t)
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("forcefield home perm = %04o, want 0700", perm)
	}
	// Also ensure Dir fixes pre-existing permissive directory.
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatalf("Chmod home 0755: %v", err)
	}
	// Dir creates ~/.forcefield; ensure it repairs a permissive home's child.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod dir 0755: %v", err)
	}
	dir2, err := Dir()
	if err != nil {
		t.Fatalf("Dir() second call: %v", err)
	}
	info, err = os.Stat(dir2)
	if err != nil {
		t.Fatalf("Stat dir2: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("after repair, perm = %04o, want 0700", perm)
	}
}

func TestLoadCreatesConfigWithRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	isolateHome(t)
	_, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	path := mustPath(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config.yaml perm = %04o, want 0600", perm)
	}
}

func TestSaveCreatesRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	isolateHome(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	path := mustPath(t)
	// Remove file to test fresh create.
	_ = os.Remove(path)
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after Save: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("after Save, perm = %04o, want 0600", perm)
	}
}

func TestSavePreservesExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	isolateHome(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	path := mustPath(t)
	// Force a distinct mode and verify Save preserves it.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod 0600: %v", err)
	}
	cfg.Agent.Name = "preserve-test"
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("preserved perm = %04o, want 0600", perm)
	}
	// Also test preserving a more permissive mode (e.g. 0644) to ensure
	// atomic write does not unexpectedly loosen a private file, but also
	// does not fail when file is permissive.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod 0644: %v", err)
	}
	cfg.Agent.Name = "preserve-test-2"
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() second: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("Stat second: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("preserved 0644 perm = %04o, want 0644", perm)
	}
}

func TestWriteFileAtomicDoesNotLeavePermissiveTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	isolateHome(t)
	// Ensure the directory for writeFileAtomic exists via Dir.
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	path := filepath.Join(dir, "atomic-perm-test.yaml")
	// Fresh file should be 0600.
	if err := writeFileAtomic(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("atomic file perm = %04o, want 0600", perm)
	}
	// Ensure no temp file is left with permissive bits; by checking dir
	// contains no .tmp files at all (also tested elsewhere).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	// Check no leftover temp pattern.
	for _, e := range entries {
		if len(e.Name()) > 3 && contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
