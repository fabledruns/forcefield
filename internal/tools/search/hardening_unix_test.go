//go:build !windows

package search

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"forcefield/internal/sandbox"
)

// A FIFO inside the walked tree must be skipped without blocking: the
// no-follow open plus descriptor check rejects it before any read.
func TestSearchFiles_FIFOSkippedWithoutBlocking(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hit.txt"), []byte("needle here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(ws, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	s := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: ws})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := s.Execute(ctx, map[string]any{"pattern": "needle", "path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("search with FIFO present must complete, got %q", res.Content)
	}
}
