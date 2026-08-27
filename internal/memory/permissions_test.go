package memory

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMemorySaveRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	dir := t.TempDir()
	store := newStore(filepath.Join(dir, "mem", "store.json"))
	if _, _, err := store.Add("hello world"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "mem"))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("memory dir perm = %04o, want 0700", perm)
	}
	info, err = os.Stat(filepath.Join(dir, "mem", "store.json"))
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("memory file perm = %04o, want 0600", perm)
	}
}

func TestMemorySavePreservesExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	dir := t.TempDir()
	store := newStore(filepath.Join(dir, "mem2", "store.json"))
	if _, _, err := store.Add("first"); err != nil {
		t.Fatalf("Add first: %v", err)
	}
	path := filepath.Join(dir, "mem2", "store.json")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod 0600: %v", err)
	}
	if _, _, err := store.Add("second"); err != nil {
		t.Fatalf("Add second: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("preserved perm = %04o, want 0600", perm)
	}
	// Preserve 0644.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod 0644: %v", err)
	}
	if _, _, err := store.Add("third"); err != nil {
		t.Fatalf("Add third: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("Stat third: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("preserved 0644 perm = %04o, want 0644", perm)
	}
}

func TestProjectStoreDirectoryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	home := t.TempDir()
	store, err := ProjectStore(home, "/tmp/myproject")
	if err != nil {
		t.Fatalf("ProjectStore: %v", err)
	}
	// Add entry to force directory creation and file write.
	if _, _, err := store.Add("fact"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	memDir := filepath.Join(home, "memory")
	info, err := os.Stat(memDir)
	if err != nil {
		t.Fatalf("Stat memory dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("memory dir perm = %04o, want 0700", perm)
	}
	projDir := filepath.Join(home, "memory", "projects")
	info, err = os.Stat(projDir)
	if err != nil {
		t.Fatalf("Stat projects dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("projects dir perm = %04o, want 0700", perm)
	}
	info, err = os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat store file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store file perm = %04o, want 0600", perm)
	}

	// GlobalStore directory as well.
	home2 := t.TempDir()
	gstore, err := GlobalStore(home2)
	if err != nil {
		t.Fatalf("GlobalStore: %v", err)
	}
	if _, _, err := gstore.Add("global fact"); err != nil {
		t.Fatalf("Add global: %v", err)
	}
	info, err = os.Stat(filepath.Join(home2, "memory"))
	if err != nil {
		t.Fatalf("Stat global memory dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("global memory dir perm = %04o, want 0700", perm)
	}
}
