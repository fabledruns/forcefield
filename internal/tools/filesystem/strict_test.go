package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"forcefield/internal/sandbox"
)

// strictPolicy returns a native+strict policy rooted at dir: the shape
// the runtime wires in when workspace.mode=strict. Unlike the WSL test
// policy, this exercises the unified Confines() path on the native
// backend on every platform.
func strictPolicy(dir string) sandbox.Policy {
	return sandbox.Policy{Mode: sandbox.ModeNative, Workspace: dir, Strict: true}
}

func TestStrict_ReadInsideOkOutsideDenied(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "in.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outPath, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	rf := NewReadFileWithPolicy(strictPolicy(ws))

	res, err := rf.Execute(context.Background(), map[string]any{"path": "in.txt"})
	if err != nil || res.IsError || res.Content != "hi" {
		t.Fatalf("inside read = %+v err=%v, want hi", res, err)
	}
	if res, err := rf.Execute(context.Background(), map[string]any{"path": outPath}); err != nil || !res.IsError {
		t.Fatalf("absolute outside read = %+v err=%v, want denial", res, err)
	}
	if res, err := rf.Execute(context.Background(), map[string]any{"path": "../escape.txt"}); err != nil || !res.IsError {
		t.Fatalf("traversal read = %+v err=%v, want denial", res, err)
	}
	// Absolute inside is accepted.
	if res, err := rf.Execute(context.Background(), map[string]any{"path": filepath.Join(ws, "in.txt")}); err != nil || res.IsError {
		t.Fatalf("absolute inside read = %+v err=%v, want success", res, err)
	}
	// The workspace root itself reads as a directory error, not an escape.
	if res, err := rf.Execute(context.Background(), map[string]any{"path": ws}); err != nil || !res.IsError {
		t.Fatalf("root read = %+v err=%v, want a domain error", res, err)
	} else if !strings.Contains(res.Content, "is a directory") {
		t.Errorf("root must read as a directory, got: %q", res.Content)
	}
}

func TestStrict_WriteInsideOkOutsideDenied(t *testing.T) {
	ws := t.TempDir()
	wf := NewWriteFileWithPolicy(strictPolicy(ws))

	res, err := wf.Execute(context.Background(), map[string]any{"path": "new/note.txt", "content": "hi"})
	if err != nil || res.IsError {
		t.Fatalf("inside write = %+v err=%v, want success", res, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "new", "note.txt")); err != nil {
		t.Fatalf("written file missing: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "evil.txt")
	if res, err := wf.Execute(context.Background(), map[string]any{"path": outside, "content": "x"}); err != nil || !res.IsError {
		t.Fatalf("outside write = %+v err=%v, want denial", res, err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("outside file was created despite denial")
	}
	if res, err := wf.Execute(context.Background(), map[string]any{"path": "../evil.txt", "content": "x"}); err != nil || !res.IsError {
		t.Fatalf("traversal write = %+v err=%v, want denial", res, err)
	}
}

func TestStrict_ListInsideOkOutsideDenied(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lf := NewListFilesWithPolicy(strictPolicy(ws))

	res, err := lf.Execute(context.Background(), map[string]any{"path": "."})
	if err != nil || res.IsError || !strings.Contains(res.Content, "a.txt") {
		t.Fatalf("inside list = %+v err=%v, want a.txt", res, err)
	}
	if res, err := lf.Execute(context.Background(), map[string]any{"path": t.TempDir()}); err != nil || !res.IsError {
		t.Fatalf("outside list = %+v err=%v, want denial", res, err)
	}
	if res, err := lf.Execute(context.Background(), map[string]any{"path": ".."}); err != nil || !res.IsError {
		t.Fatalf("traversal list = %+v err=%v, want denial", res, err)
	}
}

func TestStrict_PermissiveRegression(t *testing.T) {
	// Without Strict, the same native tools keep historical behavior:
	// absolute outside paths are accepted (existence-only checks).
	outside := t.TempDir()
	outPath := filepath.Join(outside, "open.txt")
	if err := os.WriteFile(outPath, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	rf := NewReadFile() // zero policy: permissive native
	res, err := rf.Execute(context.Background(), map[string]any{"path": outPath})
	if err != nil || res.IsError || res.Content != "hi" {
		t.Fatalf("permissive read = %+v err=%v, want historical acceptance", res, err)
	}
	if (sandbox.Policy{}).Confines() {
		t.Fatal("zero Policy must not confine")
	}
}

func TestStrict_ConcurrentReadsStayBounded(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "in.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	rf := NewReadFileWithPolicy(strictPolicy(ws))

	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "in.txt"
			wantErr := false
			if i%2 == 1 {
				path = outside
				wantErr = true
			}
			res, err := rf.Execute(context.Background(), map[string]any{"path": path})
			if err != nil {
				errs <- err.Error()
				return
			}
			if res.IsError != wantErr {
				errs <- "mismatch for " + path
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
