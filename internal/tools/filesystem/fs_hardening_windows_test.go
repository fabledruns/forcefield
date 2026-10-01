//go:build windows

package filesystem

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func mklinkJunction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J unavailable: %v (%s)", err, out)
	}
}

// NTFS junctions are chased by EvalLinks pre-resolution: reads and
// writes through a junction escaping the workspace are denied. There
// is no O_NOFOLLOW on Windows, so this pre-resolution plus the Lstat
// pre-open check is the enforcement (honest limitation, not equivalence
// with Unix).
func TestReadFile_JunctionEscapeRefused(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	mklinkJunction(t, filepath.Join(ws, "junct"), outside)
	rf := NewReadFileWithPolicy(testPolicy(ws))
	res, err := rf.Execute(context.Background(), map[string]any{"path": filepath.Join("junct", "secret.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("junction read must be denied, got %q", res.Content)
	}
}

func TestWriteFile_JunctionEscapeRefused(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	mklinkJunction(t, filepath.Join(ws, "junct"), outside)
	wf := NewWriteFileWithPolicy(testPolicy(ws))
	res, err := wf.Execute(context.Background(), map[string]any{"path": filepath.Join("junct", "new.txt"), "content": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("junction write must be denied")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("write escaped through junction")
	}
}
