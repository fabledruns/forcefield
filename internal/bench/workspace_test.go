package bench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceManager_PrepareAndCleanup(t *testing.T) {
	tmpBase := t.TempDir()
	mgr := NewWorkspaceManager(tmpBase, false, false)

	task := &Task{
		ID:       "TEST-WS",
		Category: CategoryDebugging,
		Fixtures: map[string]string{
			"hello.txt":  "hello world",
			"sub/foo.go": "package sub\n",
		},
		CanonicalFiles: map[string]string{
			"canonical.txt": "canonical content",
		},
	}

	ws, err := mgr.Prepare(context.Background(), task, "run-1")
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if ws.InitialCommit == "" {
		t.Fatal("expected non-empty InitialCommit")
	}

	// Verify fixtures exist
	hContent, err := os.ReadFile(filepath.Join(ws.Dir, "hello.txt"))
	if err != nil || string(hContent) != "hello world" {
		t.Fatalf("fixture hello.txt mismatch: %v, %s", err, string(hContent))
	}

	// Modify a file
	if err := os.WriteFile(filepath.Join(ws.Dir, "hello.txt"), []byte("modified hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Add an untracked file
	if err := os.WriteFile(filepath.Join(ws.Dir, "new.txt"), []byte("new file"), 0644); err != nil {
		t.Fatal(err)
	}

	files, diff, err := mgr.Diff(context.Background(), ws)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("Diff returned %d files, want at least 2 (%v)", len(files), files)
	}
	if diff == "" {
		t.Fatal("expected non-empty diff")
	}

	// Test RestoreCanonical
	if err := mgr.RestoreCanonical(ws); err != nil {
		t.Fatalf("RestoreCanonical failed: %v", err)
	}
	cContent, err := os.ReadFile(filepath.Join(ws.Dir, "canonical.txt"))
	if err != nil || string(cContent) != "canonical content" {
		t.Fatalf("canonical file mismatch: %v, %s", err, string(cContent))
	}

	// Test Cleanup with passed=true (should delete)
	if err := mgr.Cleanup(ws, true); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}
	if _, err := os.Stat(ws.Dir); !os.IsNotExist(err) {
		t.Fatalf("expected workspace dir %s to be removed", ws.Dir)
	}
}

func TestWorkspaceManager_RetainOnFailure(t *testing.T) {
	tmpBase := t.TempDir()
	mgr := NewWorkspaceManager(tmpBase, false, true) // keepFailed = true

	task := &Task{
		ID:       "TEST-RETAIN",
		Category: CategoryDebugging,
		Fixtures: map[string]string{"foo.txt": "bar"},
	}

	ws, err := mgr.Prepare(context.Background(), task, "run-retain")
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	// Cleanup on failure -> must retain
	if err := mgr.Cleanup(ws, false); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}
	if !ws.Retained {
		t.Fatal("expected ws.Retained to be true")
	}
	if _, err := os.Stat(ws.Dir); err != nil {
		t.Fatalf("expected retained workspace to remain on disk: %v", err)
	}

	// Clean up temp dir
	_ = os.RemoveAll(ws.Dir)
}
