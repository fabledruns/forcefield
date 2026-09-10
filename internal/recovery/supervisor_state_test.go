package recovery

import (
	"context"
	"sync"
	"testing"
	"time"

	"forcefield/internal/session"
)

// loadRetry reads a session under concurrent writers. On Windows an
// in-flight atomic rename transiently fails reads with a sharing
// violation (writes stay atomic — the file is never corrupt), so retry
// briefly before concluding anything.
func loadRetry(t *testing.T, id string) *session.Session {
	t.Helper()
	var sess *session.Session
	var err error
	for i := 0; i < 100; i++ {
		sess, err = session.Load(id)
		if err == nil {
			return sess
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Load kept failing under concurrency: %v", err)
	return nil
}

func TestSupervisorHelpersNilSafe(t *testing.T) {
	NoteSupervisorRestart(nil, 3)
	NoteSupervisorExhausted(nil)
	if ClearSupervisor(nil) {
		t.Error("ClearSupervisor(nil) reported a change")
	}
}

func TestSupervisorLifecycleRoundTrip(t *testing.T) {
	enterTempDir(t)
	sess := session.New()
	sess.AddMessage("user", "work")
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Clearing a clean session changes nothing (and must not write).
	if ClearSupervisor(sess) {
		t.Error("ClearSupervisor on clean session reported a change")
	}

	NoteSupervisorRestart(sess, 2)
	reloaded := mustLoadSupervisor(t, sess.ID)
	if reloaded.Supervisor == nil || reloaded.Supervisor.Restarts != 2 {
		t.Fatalf("Supervisor = %+v, want restarts 2", reloaded.Supervisor)
	}
	if reloaded.Supervisor.ExhaustedAt != 0 {
		t.Errorf("ExhaustedAt = %d, want unset", reloaded.Supervisor.ExhaustedAt)
	}
	// Absolute (not incremental) sets converge on replay.
	NoteSupervisorRestart(sess, 2)
	if reloaded := mustLoadSupervisor(t, sess.ID); reloaded.Supervisor.Restarts != 2 {
		t.Errorf("replayed restarts = %d, want 2", reloaded.Supervisor.Restarts)
	}
	// Negative counts normalize instead of persisting nonsense.
	NoteSupervisorRestart(sess, -4)
	if reloaded := mustLoadSupervisor(t, sess.ID); reloaded.Supervisor.Restarts != 0 {
		t.Errorf("negative restarts = %d, want 0", reloaded.Supervisor.Restarts)
	}

	before := time.Now().UTC().Unix()
	NoteSupervisorExhausted(sess)
	reloaded = mustLoadSupervisor(t, sess.ID)
	if reloaded.Supervisor == nil || reloaded.Supervisor.ExhaustedAt < before {
		t.Fatalf("Supervisor = %+v, want an exhaustion timestamp", reloaded.Supervisor)
	}

	if !ClearSupervisor(sess) {
		t.Error("ClearSupervisor on dirty session reported no change")
	}
	if reloaded := mustLoadSupervisor(t, sess.ID); reloaded.Supervisor != nil {
		t.Errorf("Supervisor = %+v, want nil after clear", reloaded.Supervisor)
	}
	// Conversation survived the bookkeeping untouched.
	if reloaded := mustLoadSupervisor(t, sess.ID); len(reloaded.Messages) != 1 {
		t.Errorf("messages = %d, want the single user message", len(reloaded.Messages))
	}
}

func mustLoadSupervisor(t *testing.T, id string) *session.Session {
	t.Helper()
	sess, err := session.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return sess
}

func TestSuperviseFromSeedsBudget(t *testing.T) {
	budget := Budget{MaxRestarts: 5, BaseBackoff: time.Second, MaxBackoff: time.Minute}
	child, calls := scriptChild(ExitRetryable, ExitRetryable, ExitRetryable, ExitOK)
	var waits []time.Duration
	got := SuperviseFrom(context.Background(), budget, 2, child, recordSleep(&waits), nil)
	if got != ExitOK {
		t.Errorf("SuperviseFrom = %d, want %d", got, ExitOK)
	}
	if *calls != 4 {
		t.Errorf("child called %d times, want 4 (2 seeded + 2 retries + success)", *calls)
	}
	// Backoff indexing continues from the seed: attempts 3,4,5 wait
	// Backoff(2), Backoff(3), Backoff(4).
	want := []time.Duration{budget.Backoff(2), budget.Backoff(3), budget.Backoff(4)}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i, w := range want {
		if waits[i] != w {
			t.Errorf("wait %d = %v, want %v", i, waits[i], w)
		}
	}
}

func TestSuperviseFromSeedExhausted(t *testing.T) {
	// used already at the cap: one attempt may still run (the run
	// itself is never the budget's business), but its retryable exit
	// spends nothing further.
	child, calls := scriptChild(ExitRetryable)
	var waits []time.Duration
	got := SuperviseFrom(context.Background(), Budget{MaxRestarts: 3}, 3, child, recordSleep(&waits), nil)
	if got != ExitRetryable {
		t.Errorf("SuperviseFrom = %d, want %d", got, ExitRetryable)
	}
	if *calls != 1 || len(waits) != 0 {
		t.Errorf("calls = %d, waits = %v; seeded-exhausted budget retries nothing", *calls, waits)
	}
}

func TestSuperviseFromNegativeSeed(t *testing.T) {
	child, calls := scriptChild(ExitRetryable, ExitOK)
	got := SuperviseFrom(context.Background(), Budget{MaxRestarts: 5}, -2, child, recordSleep(nil), nil)
	if got != ExitOK {
		t.Errorf("SuperviseFrom = %d, want %d", got, ExitOK)
	}
	if *calls != 2 {
		t.Errorf("child called %d times, want 2 (negative seed counts as zero)", *calls)
	}
}

// TestSupervisorStateConcurrentWriters is the adversarial case: several
// writers race read-modify-write on one session file. Atomic saves mean
// the file must always stay valid with the conversation intact; counts
// may lose increments (bounded extra restarts, each invocation still
// enforces its own budget), but must never corrupt or go negative.
func TestSupervisorStateConcurrentWriters(t *testing.T) {
	enterTempDir(t)
	sess := session.New()
	sess.AddMessage("user", "shared work")
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const writers = 8
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				s := loadRetry(t, sess.ID)
				switch (i + j) % 3 {
				case 0:
					NoteSupervisorRestart(s, i+j)
				case 1:
					NoteSupervisorExhausted(s)
				default:
					ClearSupervisor(s)
				}
			}
		}(i)
	}
	wg.Wait()

	final := loadRetry(t, sess.ID)
	if len(final.Messages) != 1 || final.Messages[0].Content != "shared work" {
		t.Errorf("conversation damaged by lifecycle races: %+v", final.Messages)
	}
	if final.Supervisor != nil {
		if final.Supervisor.Restarts < 0 || final.Supervisor.ExhaustedAt < 0 {
			t.Errorf("Supervisor insane after races: %+v", final.Supervisor)
		}
	}
}
