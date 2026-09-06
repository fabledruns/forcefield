package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

func strictPolicy(dir string) sandbox.Policy {
	return sandbox.Policy{Mode: sandbox.ModeNative, Workspace: dir, Strict: true}
}

// TestSearch_StrictConfinesRoot pins that strict native search enforces
// the same boundary as the wsl path: traversal fails, inside works.
func TestSearch_StrictConfinesRoot(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "code.go"), "MARKER_INNER\n")
	tool := NewSearchFilesWithPolicy(strictPolicy(ws))

	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_INNER", "path": "."})
	if err != nil || res.IsError {
		t.Fatalf("inside search = %+v err=%v, want a match", res, err)
	}
	if !strings.Contains(res.Content, "code.go") {
		t.Errorf("missing match, got:\n%s", res.Content)
	}
	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "x", "path": `..\..`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("traversal outside workspace must fail, got:\n%s", res.Content)
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "o.go"), "MARKER_OUT\n")
	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_OUT", "path": outside})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("absolute outside search must fail, got:\n%s", res.Content)
	}
}

// TestSearch_StrictSymlinkAndJunctionEscape pins per-file containment
// under strict mode, including reparse points where supported.
func TestSearch_StrictSymlinkAndJunctionEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "MARKER_OUTSIDE\n")
	writeFile(t, filepath.Join(ws, "local.go"), "MARKER_LOCAL\n")

	link := filepath.Join(ws, "link.txt")
	linked := false
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), link); err == nil {
		linked = true
	}
	tool := NewSearchFilesWithPolicy(strictPolicy(ws))
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_OUTSIDE", "path": "."})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if linked && !strings.Contains(res.Content, "no matches") {
		t.Fatalf("symlink escape must be skipped, got:\n%s", res.Content)
	}
	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_LOCAL", "path": "."})
	if err != nil || res.IsError || !strings.Contains(res.Content, "local.go") {
		t.Fatalf("local search = %+v err=%v, want local.go", res, err)
	}
}

// TestFind_StrictConfinesTraversal pins the same boundary for filename
// discovery: traversal and absolute-outside roots fail, inside works,
// and per-file symlink escapes are skipped.
func TestFind_StrictConfinesTraversal(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "inner.txt"), "x")
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "outer.txt"), "x")
	link := filepath.Join(ws, "link.txt")
	linked := false
	if err := os.Symlink(filepath.Join(outside, "outer.txt"), link); err == nil {
		linked = true
	}
	tool := NewFindFilesWithPolicy(strictPolicy(ws))

	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "inner", "path": "."})
	if err != nil || res.IsError || !strings.Contains(res.Content, "inner.txt") {
		t.Fatalf("inside find = %+v err=%v, want inner.txt", res, err)
	}
	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "*", "path": `..\..`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("traversal must fail, got:\n%s", res.Content)
	}
	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "outer", "path": "."})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if linked && strings.Contains(res.Content, "link.txt") {
		t.Fatalf("symlink escape must be skipped, got:\n%s", res.Content)
	}
}
