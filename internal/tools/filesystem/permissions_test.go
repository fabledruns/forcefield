package filesystem

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileCreatesWithRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	dir := t.TempDir()
	tool := NewWriteFile()
	path := filepath.Join(dir, "newfile.txt")
	_, err := tool.Execute(nil, map[string]any{"path": path, "content": "hello"})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("new file perm = %04o, want 0600", perm)
	}
}

func TestWriteFilePreservesExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	dir := t.TempDir()
	tool := NewWriteFile()
	path := filepath.Join(dir, "preserve.txt")

	// Create with 0600 and ensure second write preserves.
	if _, err := tool.Execute(nil, map[string]any{"path": path, "content": "first"}); err != nil {
		t.Fatalf("Execute first: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod 0600: %v", err)
	}
	if _, err := tool.Execute(nil, map[string]any{"path": path, "content": "second"}); err != nil {
		t.Fatalf("Execute second: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("preserved 0600 perm = %04o, want 0600", perm)
	}

	// Preserve a more permissive existing file (e.g., 0644 created externally).
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod 0644: %v", err)
	}
	if _, err := tool.Execute(nil, map[string]any{"path": path, "content": "third"}); err != nil {
		t.Fatalf("Execute third: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("Stat third: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("preserved 0644 perm = %04o, want 0644", perm)
	}
}

func TestWriteFileNewFileIsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	dir := t.TempDir()
	tool := NewWriteFile()
	path := filepath.Join(dir, "a", "b", "secret.txt")
	_, err := tool.Execute(nil, map[string]any{"path": path, "content": "data"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm&0o044 != 0 {
		t.Errorf("new file is world/group readable: %04o", perm)
	}
}
