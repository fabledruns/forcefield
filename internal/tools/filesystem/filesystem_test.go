package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

func TestReadFile_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "greeting.txt")

	wf := NewWriteFileWithPolicy(sandbox.Policy{Workspace: dir})
	if res, err := wf.Execute(context.Background(), map[string]any{"path": path, "content": "hello"}); err != nil || res.IsError {
		t.Fatalf("setup write failed: res=%+v err=%v", res, err)
	}

	rf := NewReadFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := rf.Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute() result.IsError = true, content: %s", res.Content)
	}
	if res.Content != "hello" {
		t.Fatalf("Execute() content = %q, want %q", res.Content, "hello")
	}
}

func TestReadFile_MissingFileIsDomainError(t *testing.T) {
	// No fixture dir: the missing path resolves outside the
	// working-directory cage and is denied as a domain error either
	// way — the contract under test is "nil Go error + IsError".
	rf := NewReadFile()
	res, err := rf.Execute(context.Background(), map[string]any{"path": "/does/not/exist"})
	if err != nil {
		t.Fatalf("Execute() unexpected Go error: %v, want a domain-level Result instead", err)
	}
	if !res.IsError {
		t.Fatal("Execute() result.IsError = false, want true for a missing file")
	}
}

func TestReadFile_MissingPathArgIsGoError(t *testing.T) {
	// Missing-arg validation fails before any path resolution.
	rf := NewReadFile()
	_, err := rf.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("Execute() with missing path arg = nil error, want an error")
	}
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "file.txt")

	wf := NewWriteFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := wf.Execute(context.Background(), map[string]any{"path": path, "content": "data"})
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute() result.IsError = true, content: %s", res.Content)
	}

	rf := NewReadFileWithPolicy(sandbox.Policy{Workspace: dir})
	readBack, err := rf.Execute(context.Background(), map[string]any{"path": path})
	if err != nil || readBack.IsError {
		t.Fatalf("read-back failed: res=%+v err=%v", readBack, err)
	}
	if readBack.Content != "data" {
		t.Fatalf("read-back content = %q, want %q", readBack.Content, "data")
	}
}

func TestListFiles_DefaultsToCurrentDir(t *testing.T) {
	dir := t.TempDir()
	wf := NewWriteFileWithPolicy(sandbox.Policy{Workspace: dir})
	if _, err := wf.Execute(context.Background(), map[string]any{
		"path": filepath.Join(dir, "a.txt"), "content": "x",
	}); err != nil {
		t.Fatalf("setup write failed: %v", err)
	}

	lf := NewListFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := lf.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute() result.IsError = true, content: %s", res.Content)
	}
	if res.Content != "a.txt" {
		t.Fatalf("Execute() content = %q, want %q", res.Content, "a.txt")
	}
}

func TestListFiles_NonexistentDirIsDomainError(t *testing.T) {
	// No fixture dir: the missing path resolves outside the
	// working-directory cage and is denied as a domain error either
	// way — the contract under test is "nil Go error + IsError".
	lf := NewListFiles()
	res, err := lf.Execute(context.Background(), map[string]any{"path": "/does/not/exist"})
	if err != nil {
		t.Fatalf("Execute() unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("Execute() result.IsError = false, want true for a missing directory")
	}
}

func TestListFiles_RootPlaceholderProducesActionableDiagnostic(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalWS, err := filepath.EvalSymlinks(ws)
	if err != nil {
		canonicalWS = ws
	}

	lf := NewListFilesWithPolicy(sandbox.Policy{Workspace: ws})

	// Schema explains workspace root defaults and warns against "root".
	props := lf.InputSchema()["properties"].(map[string]any)
	pathDesc := props["path"].(map[string]any)["description"].(string)
	if !strings.Contains(pathDesc, `"."`) || !strings.Contains(pathDesc, `"root"`) {
		t.Errorf("list_files.path description should explain '.' default and warn against 'root', got: %q", pathDesc)
	}

	// Passing "root" when no subdirectory named root exists gives an actionable hint.
	res, err := lf.Execute(context.Background(), map[string]any{"path": "root"})
	if err != nil {
		t.Fatalf("Execute() unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("Execute(root) result.IsError = false, want true")
	}
	for _, want := range []string{
		"root",
		canonicalWS,
		`"."`,
		"subdirectory",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("Execute(root) Content = %q, want substring %q", res.Content, want)
		}
	}

	// Omitting path or passing "." still lists the workspace root.
	for _, args := range []map[string]any{{}, {"path": "."}} {
		okRes, err := lf.Execute(context.Background(), args)
		if err != nil || okRes.IsError || !strings.Contains(okRes.Content, "go.mod") {
			t.Fatalf("Execute(%v) = %+v err=%v, want go.mod", args, okRes, err)
		}
	}
}
