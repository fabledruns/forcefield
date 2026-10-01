package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

func testPolicy(ws string) sandbox.Policy {
	return sandbox.Policy{Workspace: ws}
}

func mustWrite(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Symlink final-component reads must be refused, never followed outside.
func TestReadFile_SymlinkFinalRefused(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	mustWrite(t, secret, "top-secret")
	link := filepath.Join(ws, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rf := NewReadFileWithPolicy(testPolicy(ws))
	res, err := rf.Execute(context.Background(), map[string]any{"path": "link.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("symlink read must be denied, got %q", res.Content)
	}
	if strings.Contains(res.Content, "top-secret") {
		t.Fatal("symlink read exposed outside content")
	}
}

// Symlink final-component writes must be refused (Lstat + no-follow).
func TestWriteFile_SymlinkFinalRefused(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	mustWrite(t, secret, "original")
	link := filepath.Join(ws, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	wf := NewWriteFileWithPolicy(testPolicy(ws))
	res, err := wf.Execute(context.Background(), map[string]any{"path": "link.txt", "content": "pwned"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("symlink write must be denied")
	}
	if got, _ := os.ReadFile(secret); string(got) != "original" {
		t.Fatal("symlink write corrupted the outside target")
	}
}

// A symlinked ancestor must fail the write before anything is created.
func TestWriteFile_SymlinkAncestorRefused(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(ws, "evildir")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	wf := NewWriteFileWithPolicy(testPolicy(ws))
	res, err := wf.Execute(context.Background(), map[string]any{
		"path": filepath.Join("evildir", "new.txt"), "content": "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("write through symlink ancestor must be denied")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("write escaped through symlink ancestor")
	}
}

// Cancelled contexts fail fast instead of blocking in open/read/write.
func TestFilesystem_CancelledCtx(t *testing.T) {
	ws := t.TempDir()
	mustWrite(t, filepath.Join(ws, "f.txt"), "hello")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rf := NewReadFileWithPolicy(testPolicy(ws))
	if res, _ := rf.Execute(ctx, map[string]any{"path": "f.txt"}); !res.IsError || !strings.Contains(res.Content, "context canceled") {
		t.Errorf("cancelled read = %+v, want context-canceled soft error", res)
	}
	wf := NewWriteFileWithPolicy(testPolicy(ws))
	if res, _ := wf.Execute(ctx, map[string]any{"path": "g.txt", "content": "x"}); !res.IsError || !strings.Contains(res.Content, "context canceled") {
		t.Errorf("cancelled write = %+v, want context-canceled soft error", res)
	}
	lf := NewListFilesWithPolicy(testPolicy(ws))
	if res, _ := lf.Execute(ctx, map[string]any{"path": "."}); !res.IsError || !strings.Contains(res.Content, "context canceled") {
		t.Errorf("cancelled list = %+v, want context-canceled soft error", res)
	}
}

// Oversized payloads are refused before allocation/write.
func TestWriteFile_OversizedRefused(t *testing.T) {
	ws := t.TempDir()
	wf := NewWriteFileWithPolicy(testPolicy(ws))
	big := strings.Repeat("A", tools.DefaultWriteMaxBytes+1)
	res, err := wf.Execute(context.Background(), map[string]any{"path": "big.txt", "content": big})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "limit") {
		t.Fatalf("oversized write must be refused with a limit note, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(ws, "big.txt")); !os.IsNotExist(err) {
		t.Fatal("oversized write created a file")
	}
}

// Oversized reads are refused with structured metadata, not partially read.
func TestReadFile_OversizedRefused(t *testing.T) {
	ws := t.TempDir()
	p := filepath.Join(ws, "big.txt")
	mustWrite(t, p, strings.Repeat("B", 200))
	rf := NewReadFileWithPolicy(testPolicy(ws))
	rf.SetLimits(tools.Limits{MaxBytes: 10})
	res, err := rf.Execute(context.Background(), map[string]any{"path": "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || res.Metadata == nil {
		t.Fatalf("oversized read must carry truncation metadata, got %+v", res)
	}
}

// New files default to restrictive permissions (Unix-only assertion;
// permission bits are not meaningful on Windows).
func TestWriteFile_DefaultPermRestrictive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not meaningful on Windows")
	}
	ws := t.TempDir()
	wf := NewWriteFileWithPolicy(testPolicy(ws))
	res, err := wf.Execute(context.Background(), map[string]any{"path": "n.txt", "content": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("write failed: %s", res.Content)
	}
	info, err := os.Stat(filepath.Join(ws, "n.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("new file perm = %o, want 600", perm)
	}
}
