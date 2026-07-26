package filesystem

import (
	"context"
	"path/filepath"
	"testing"
)

func TestReadFile_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "greeting.txt")

	wf := NewWriteFile()
	if res, err := wf.Execute(context.Background(), map[string]any{"path": path, "content": "hello"}); err != nil || res.IsError {
		t.Fatalf("setup write failed: res=%+v err=%v", res, err)
	}

	rf := NewReadFile()
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
	rf := NewReadFile()
	_, err := rf.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("Execute() with missing path arg = nil error, want an error")
	}
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "file.txt")

	wf := NewWriteFile()
	res, err := wf.Execute(context.Background(), map[string]any{"path": path, "content": "data"})
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute() result.IsError = true, content: %s", res.Content)
	}

	rf := NewReadFile()
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
	wf := NewWriteFile()
	if _, err := wf.Execute(context.Background(), map[string]any{
		"path": filepath.Join(dir, "a.txt"), "content": "x",
	}); err != nil {
		t.Fatalf("setup write failed: %v", err)
	}

	lf := NewListFiles()
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
	lf := NewListFiles()
	res, err := lf.Execute(context.Background(), map[string]any{"path": "/does/not/exist"})
	if err != nil {
		t.Fatalf("Execute() unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("Execute() result.IsError = false, want true for a missing directory")
	}
}
