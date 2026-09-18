package tui

import (
	"os"
	"strings"
	"testing"

	"forcefield/internal/session"
)

// blockSessionDir makes every session Save fail deterministically with a
// real I/O error (a file where the sessions directory must be), inside
// an isolated temp working directory so no debris escapes the test.
func blockSessionDir(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".forcefield", []byte("blocker"), 0o644); err != nil {
		t.Fatalf("plant sessions-dir blocker: %v", err)
	}
}

// failingSession returns a session whose teardown attempts a save: the
// in-progress turn forces CancelAndRepair to persist (and fail).
func failingSession() *session.Session {
	sess := session.New()
	sess.BeginTurn()
	return sess
}

func saveErrorEntries(m model) int {
	n := 0
	for _, e := range m.entries {
		if e.Role == roleError && strings.Contains(e.Content, "session save failed") {
			n++
		}
	}
	return n
}

// TestStopStreamWarnsOnSaveFailureOnce pins that a live run whose
// history stops reaching disk is never silent: teardown surfaces the
// failure in the transcript exactly once per distinct error, even
// though steady-state saves are fire-and-forget by design.
func TestStopStreamWarnsOnSaveFailureOnce(t *testing.T) {
	blockSessionDir(t)
	m := newTestModel()
	m.session = failingSession()

	m.stopStream(true)
	if m.session.LastSaveError == "" {
		t.Fatal("teardown with a failing session must record LastSaveError")
	}
	if got := saveErrorEntries(m); got != 1 {
		t.Fatalf("save-error entries = %d, want exactly 1 warning", got)
	}

	// A second teardown while the same failure persists must not spam
	// the transcript.
	m.stopStream(true)
	if got := saveErrorEntries(m); got != 1 {
		t.Fatalf("save-error entries after repeat teardown = %d, want still 1", got)
	}
}

// TestStopStreamWarnsAgainAfterRecovery pins the reset: once saves
// succeed again, a later recurrence is still reported instead of being
// swallowed by the earlier notification.
func TestStopStreamWarnsAgainAfterRecovery(t *testing.T) {
	blockSessionDir(t)
	m := newTestModel()
	m.session = failingSession()
	m.stopStream(true)
	if got := saveErrorEntries(m); got != 1 {
		t.Fatalf("save-error entries = %d, want 1", got)
	}

	// A session with nothing to persist observes a clean save state,
	// resetting the once-per-error latch (nothing changed, so teardown
	// persists nothing and LastSaveError stays empty).
	m.session = session.New()
	m.stopStream(true)
	if got := saveErrorEntries(m); got != 1 {
		t.Fatalf("save-error entries after healthy teardown = %d, want still 1", got)
	}

	// Recurrence warns again.
	m.session = failingSession()
	m.stopStream(true)
	if got := saveErrorEntries(m); got != 2 {
		t.Fatalf("save-error entries after recurrence = %d, want 2", got)
	}
}
