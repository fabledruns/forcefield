package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"forcefield/internal/memory"
)

func newTestMemoryStore(t *testing.T) *memory.Store {
	t.Helper()
	home := t.TempDir()
	store, err := memory.ProjectStore(home, filepath.Join(home, "project"))
	if err != nil {
		t.Fatalf("ProjectStore: %v", err)
	}
	return store
}

func TestAddProjectMemoryToolPersistsEntry(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := newAddProjectMemoryTool(store)

	result, err := tool.Execute(context.Background(), map[string]any{"text": "backend runs on go 1.24"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", result.Content)
	}

	entries, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 || entries[0].Text != "backend runs on go 1.24" {
		t.Fatalf("expected the tool call to persist the entry, got %+v", entries)
	}
}

func TestAddProjectMemoryToolPreventsDuplicates(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := newAddProjectMemoryTool(store)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{"text": "uses pnpm"}); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{"text": "uses pnpm"}); err != nil {
		t.Fatalf("second Execute: %v", err)
	}

	entries, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the duplicate call not to create a second entry, got %d entries", len(entries))
	}
}

func TestAddProjectMemoryToolRequiresText(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := newAddProjectMemoryTool(store)

	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatalf("expected an error when text is missing")
	}
}
