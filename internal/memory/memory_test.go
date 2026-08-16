package memory

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStoreAddLoad(t *testing.T) {
	store := newStore(filepath.Join(t.TempDir(), "mem.json"))

	entries, err := store.Load()
	if err != nil {
		t.Fatalf("Load on empty store: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %d", len(entries))
	}

	entry, added, err := store.Add("uses go 1.24")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !added {
		t.Fatalf("expected added=true for a new entry")
	}
	if entry.ID == "" {
		t.Fatalf("expected a non-empty ID")
	}
	if entry.Text != "uses go 1.24" {
		t.Fatalf("unexpected text: %q", entry.Text)
	}
	if entry.CreatedAt.IsZero() {
		t.Fatalf("expected a non-zero CreatedAt")
	}

	entries, err = store.Load()
	if err != nil {
		t.Fatalf("Load after Add: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("expected the persisted entry to round-trip, got %+v", entries)
	}
}

func TestStoreAddRejectsEmptyText(t *testing.T) {
	store := newStore(filepath.Join(t.TempDir(), "mem.json"))

	if _, _, err := store.Add("   "); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("expected ErrEmptyText, got %v", err)
	}
}

func TestStoreAddPreventsDuplicates(t *testing.T) {
	store := newStore(filepath.Join(t.TempDir(), "mem.json"))

	first, added, err := store.Add("run tests with make test")
	if err != nil || !added {
		t.Fatalf("first Add: entry=%+v added=%v err=%v", first, added, err)
	}

	// Exact duplicate.
	dup, added, err := store.Add("run tests with make test")
	if err != nil {
		t.Fatalf("duplicate Add returned error: %v", err)
	}
	if added {
		t.Fatalf("expected duplicate Add to report added=false")
	}
	if dup.ID != first.ID {
		t.Fatalf("expected the duplicate to resolve to the original entry")
	}

	// Same text modulo surrounding whitespace and case.
	dup2, added, err := store.Add("  Run Tests With Make Test  ")
	if err != nil {
		t.Fatalf("normalized duplicate Add returned error: %v", err)
	}
	if added {
		t.Fatalf("expected normalized duplicate to report added=false")
	}
	if dup2.ID != first.ID {
		t.Fatalf("expected normalized duplicate to resolve to the original entry")
	}

	entries, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one persisted entry, got %d", len(entries))
	}
}

func TestStoreAddAssignsDistinctIDs(t *testing.T) {
	store := newStore(filepath.Join(t.TempDir(), "mem.json"))

	a, _, err := store.Add("fact one")
	if err != nil {
		t.Fatalf("Add a: %v", err)
	}
	b, _, err := store.Add("fact two")
	if err != nil {
		t.Fatalf("Add b: %v", err)
	}

	if a.ID == b.ID {
		t.Fatalf("expected distinct IDs, got %q twice", a.ID)
	}
}

func TestStoreRemove(t *testing.T) {
	store := newStore(filepath.Join(t.TempDir(), "mem.json"))

	entry, _, err := store.Add("fact to remove")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, _, err := store.Add("fact to keep"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := store.Remove(entry.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	entries, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 || entries[0].Text != "fact to keep" {
		t.Fatalf("unexpected entries after Remove: %+v", entries)
	}
}

func TestStoreRemoveNotFound(t *testing.T) {
	store := newStore(filepath.Join(t.TempDir(), "mem.json"))

	if _, _, err := store.Add("some fact"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	err := store.Remove("does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	entries, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected Remove of an unknown id to leave existing entries untouched, got %d", len(entries))
	}
}

func TestStoreClear(t *testing.T) {
	store := newStore(filepath.Join(t.TempDir(), "mem.json"))

	for _, text := range []string{"fact one", "fact two", "fact three"} {
		if _, _, err := store.Add(text); err != nil {
			t.Fatalf("Add(%q): %v", text, err)
		}
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	entries, err := store.Load()
	if err != nil {
		t.Fatalf("Load after Clear: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries after Clear, got %d", len(entries))
	}
}

func TestStoreLoadMissingFile(t *testing.T) {
	// No Add has happened, so the backing file was never created.
	store := newStore(filepath.Join(t.TempDir(), "does-not-exist.json"))

	entries, err := store.Load()
	if err != nil {
		t.Fatalf("Load on a missing file should not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %d", len(entries))
	}
}

func TestFormatForPrompt(t *testing.T) {
	if got := FormatForPrompt(nil); got != "" {
		t.Fatalf("expected empty string for no entries, got %q", got)
	}

	entries := []Entry{
		{ID: "1", Text: "uses pnpm, not npm"},
		{ID: "2", Text: "tests live under internal/*/*_test.go"},
	}
	got := FormatForPrompt(entries)
	want := "- uses pnpm, not npm\n- tests live under internal/*/*_test.go"
	if got != want {
		t.Fatalf("FormatForPrompt mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}
