package hardening

import (
	"sync"
	"testing"

	"forcefield/internal/session"
)

// P1.15 lab: concurrency. Detects last-wins loss, corruption, deadlocks.
func TestConcurrentSessionSavesDoNotCorrupt(t *testing.T) {
	// Sessions are cwd-relative (.forcefield/sessions); isolate with Chdir.
	t.Chdir(t.TempDir())
	const writers = 8
	sessions := make([]*session.Session, writers)
	for w := 0; w < writers; w++ {
		sessions[w] = session.New()
		sessions[w].AddMessage("user", "initial")
		if err := sessions[w].Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, writers*25)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// Each goroutine owns its session: concurrent saves of
			// distinct sessions must never corrupt each other.
			for i := 0; i < 25; i++ {
				sessions[w].AddMessage("user", "writer message")
				if err := sessions[w].Save(); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Save error: %v", err)
	}
	// Every file must remain parseable.
	for w := 0; w < writers; w++ {
		loaded, err := session.Load(sessions[w].ID)
		if err != nil {
			t.Fatalf("Load after concurrent saves: %v", err)
		}
		if len(loaded.Messages) == 0 {
			t.Fatalf("session %d lost all messages", w)
		}
	}
}

func TestSameSessionConcurrentSaveLastWinsDocumented(t *testing.T) {
	// Same-session concurrent writes are last-wins by design (no file
	// locking). This test proves the file stays parseable; P1.18 must add
	// in-process serialization + cross-process documentation.
	t.Chdir(t.TempDir())
	base := session.New()
	base.AddMessage("user", "base")
	if err := base.Save(); err != nil {
		t.Fatal(err)
	}
	// Two writers load the same session and save divergently.
	a, err := session.Load(base.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := session.Load(base.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.AddMessage("user", "writer A message")
	b.AddMessage("user", "writer B message")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = a.Save() }()
	go func() { defer wg.Done(); _ = b.Save() }()
	wg.Wait()
	final, err := session.Load(base.ID)
	if err != nil {
		t.Fatalf("Load after divergent saves: %v", err)
	}
	// One writer wins; the file must still parse with at least base+1.
	if len(final.Messages) < 2 {
		t.Fatalf("session lost base messages: %d", len(final.Messages))
	}
	t.Logf("divergent saves converged to %d messages (last-wins, parseable)", len(final.Messages))
}
