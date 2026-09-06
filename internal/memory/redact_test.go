package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddScrubsSecrets pins that a fact echoing credentials is sanitized
// before dedup and before it reaches the memory file.
func TestAddScrubsSecrets(t *testing.T) {
	dir := t.TempDir()
	store := newStore(filepath.Join(dir, "memory.json"))
	entry, added, err := store.Add(`deploy token = "hunter2-deploy-token" for staging`)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !added {
		t.Fatal("added = false, want true")
	}
	if strings.Contains(entry.Text, "hunter2-deploy-token") {
		t.Errorf("entry leaked secret: %q", entry.Text)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "memory.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "hunter2-deploy-token") {
		t.Errorf("memory file leaked secret:\n%s", raw)
	}
}
