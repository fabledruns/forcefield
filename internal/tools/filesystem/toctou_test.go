package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/sandbox"
)

func TestReadFile_WSL_SymlinkFinalComponentDenied(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Create a file inside workspace, then replace it with a symlink to outside
	inside := filepath.Join(ws, "target.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Replace inside file with symlink to outside
	if err := os.Remove(inside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, inside); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	p := sandbox.Policy{Mode: sandbox.ModeWSL, Workspace: ws}
	rf := NewReadFileWithPolicy(p)
	res, err := rf.Execute(nil, map[string]any{"path": "target.txt"})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	// With O_NOFOLLOW, opening a symlink file should fail, not follow to outside
	if !res.IsError {
		t.Fatalf("expected symlink read to be denied, got success with %q", res.Content)
	}
	if res.Content == "top-secret" {
		t.Errorf("symlink read should not expose outside content")
	}
}
