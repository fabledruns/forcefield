package hardening

import (
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/session"
)

// P1.15 lab: persistence crash boundaries. Atomic rename means readers see
// old-or-new, never torn; corruption is surfaced, never silently accepted.
func TestCorruptSessionSurfacedNotAccepted(t *testing.T) {
	t.Chdir(t.TempDir())
	sess := session.New()
	sess.AddMessage("user", "hello")
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(".forcefield", "sessions", sess.ID+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Load(sess.ID); err == nil {
		t.Fatalf("corrupt session silently accepted")
	}
	sessions, corrupts, err := session.ListCorrupt()
	if err != nil {
		t.Fatalf("ListCorrupt: %v", err)
	}
	if len(corrupts) == 0 {
		t.Fatalf("corruption not reported via ListCorrupt")
	}
	_ = sessions
}

func TestSessionCompactionBoundedAndObservable(t *testing.T) {
	// Reproduction: 1000-msg silent-drop compaction discards user data
	// without a marker. P1.19 must make the policy explicit + observable.
	t.Chdir(t.TempDir())
	sess := session.New()
	for i := 0; i < 1200; i++ {
		sess.AddMessage("user", "filler message for growth")
	}
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := session.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) > 1000 {
		t.Fatalf("session grew past cap: %d", len(loaded.Messages))
	}
	// Marker requirement: compaction must leave an observable record.
	found := false
	for _, m := range loaded.Messages {
		if len(m.Content) >= 11 && (contains(m.Content, "compacted") || contains(m.Content, "truncated")) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("compaction dropped %d messages with no observable marker (silent data loss)", 1200-len(loaded.Messages))
	}
}

func TestPartialTmpNeverParsed(t *testing.T) {
	t.Chdir(t.TempDir())
	sess := session.New()
	sess.AddMessage("user", "base")
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}
	// Crash-leftover tmp debris must never be parsed as a session.
	dir := filepath.Join(".forcefield", "sessions")
	if err := os.WriteFile(filepath.Join(dir, sess.ID+".json.tmp-orphan"), []byte("{partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, _, err := session.ListCorrupt()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.ID == sess.ID+".json.tmp-orphan" {
			t.Fatalf("tmp debris parsed as session")
		}
	}
}
