package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/sandbox"
)

// Cancelled contexts abort the walk with a soft cancelled result.
func TestSearchFiles_CancelledCtx(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: ws})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := s.Execute(ctx, map[string]any{"pattern": "hello", "path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Errorf("cancelled search must be a soft error, got %+v", res)
	}
}
