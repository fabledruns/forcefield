package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/recovery"
	"forcefield/internal/session"
)

// enterSuperviseTempDir runs the test with the working directory in a
// fresh temp dir, since sessions persist under ./.forcefield/sessions.
func enterSuperviseTempDir(t *testing.T) string {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})
	return dir
}

// seedSession creates a saved session, optionally with supervisor
// lifecycle state, and returns its id.
func seedSession(t *testing.T, state *session.SupervisorState) string {
	t.Helper()
	sess := session.New()
	sess.AddMessage("user", "supervised work")
	sess.Supervisor = state
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return sess.ID
}

func loadSupervisor(t *testing.T, id string) *session.Session {
	t.Helper()
	sess, err := session.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return sess
}

func sessionTestBudget() recovery.Budget {
	return recovery.Budget{MaxRestarts: 5, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
}

func TestSuperviseRefusesExhaustedEpisode(t *testing.T) {
	isolateSuperviseGlobals(t)
	enterSuperviseTempDir(t)
	id := seedSession(t, &session.SupervisorState{Restarts: 5, ExhaustedAt: time.Now().UTC().Unix()})

	var calls [][2]any
	superviseSpawn = scriptSpawn([]int{0}, &calls)
	if got := superviseSessionCommand(context.Background(), id, 0, sessionTestBudget(), false); got != recovery.ExitTerminal {
		t.Errorf("superviseSessionCommand = %d, want %d (refuse without spending)", got, recovery.ExitTerminal)
	}
	if len(calls) != 0 {
		t.Errorf("spawned %d children for a latched episode, want none", len(calls))
	}
	// The latch survives the refusal.
	if st := loadSupervisor(t, id).Supervisor; st == nil || st.ExhaustedAt == 0 {
		t.Errorf("Supervisor = %+v, want the latch intact", st)
	}
}

func TestSuperviseResetBudgetStartsNewEpisode(t *testing.T) {
	isolateSuperviseGlobals(t)
	enterSuperviseTempDir(t)
	id := seedSession(t, &session.SupervisorState{Restarts: 5, ExhaustedAt: time.Now().UTC().Unix()})

	var calls [][2]any
	superviseSpawn = scriptSpawn([]int{0}, &calls)
	if got := superviseSessionCommand(context.Background(), id, 0, sessionTestBudget(), true); got != 0 {
		t.Errorf("superviseSessionCommand = %d, want 0", got)
	}
	if len(calls) != 1 {
		t.Fatalf("attempts = %d, want 1", len(calls))
	}
	// Observed success clears the episode.
	if st := loadSupervisor(t, id).Supervisor; st != nil {
		t.Errorf("Supervisor = %+v, want nil after success", st)
	}
}

func TestSuperviseContinuesRemainingBudget(t *testing.T) {
	isolateSuperviseGlobals(t)
	enterSuperviseTempDir(t)
	// Killed during backoff with 2 of 3 restarts spent: the next
	// invocation continues with the remainder, not a fresh budget.
	id := seedSession(t, &session.SupervisorState{Restarts: 2})

	var calls [][2]any
	superviseSpawn = scriptSpawn([]int{3, 0}, &calls)
	budget := recovery.Budget{MaxRestarts: 3, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	if got := superviseSessionCommand(context.Background(), id, 0, budget, false); got != 0 {
		t.Errorf("superviseSessionCommand = %d, want 0", got)
	}
	if len(calls) != 2 {
		t.Fatalf("attempts = %d, want 2 (one restart left, then success)", len(calls))
	}
	if st := loadSupervisor(t, id).Supervisor; st != nil {
		t.Errorf("Supervisor = %+v, want nil after success", st)
	}
}

func TestSuperviseSeededCounterExhausts(t *testing.T) {
	isolateSuperviseGlobals(t)
	enterSuperviseTempDir(t)
	id := seedSession(t, &session.SupervisorState{Restarts: 3})

	var calls [][2]any
	superviseSpawn = scriptSpawn([]int{3}, &calls)
	budget := recovery.Budget{MaxRestarts: 3, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	if got := superviseSessionCommand(context.Background(), id, 0, budget, false); got != recovery.ExitRetryable {
		t.Errorf("superviseSessionCommand = %d, want %d", got, recovery.ExitRetryable)
	}
	// The seeded budget allowed the attempt itself but no further
	// restart; the latch is now set for the next invocation.
	if len(calls) != 1 {
		t.Fatalf("attempts = %d, want 1", len(calls))
	}
	st := loadSupervisor(t, id).Supervisor
	if st == nil || st.ExhaustedAt == 0 {
		t.Fatalf("Supervisor = %+v, want a latched episode", st)
	}
	if st.Restarts != 3 {
		t.Errorf("Restarts = %d, want the seeded 3", st.Restarts)
	}
}

func TestSuperviseTerminalClearsEpisode(t *testing.T) {
	isolateSuperviseGlobals(t)
	enterSuperviseTempDir(t)

	for _, code := range []int{recovery.ExitTerminal, recovery.ExitNeedsHuman} {
		id := seedSession(t, &session.SupervisorState{Restarts: 2})
		var calls [][2]any
		superviseSpawn = scriptSpawn([]int{code}, &calls)
		if got := superviseSessionCommand(context.Background(), id, 0, sessionTestBudget(), false); got != code {
			t.Errorf("code %d: superviseSessionCommand = %d", code, got)
		}
		if st := loadSupervisor(t, id).Supervisor; st != nil {
			t.Errorf("code %d: Supervisor = %+v, want nil (terminal clears)", code, st)
		}
	}
}

func TestSuperviseTerminalNeverCreatesRetryState(t *testing.T) {
	isolateSuperviseGlobals(t)
	enterSuperviseTempDir(t)
	// Quota/auth classify to exit 2 in the child (see recovery exit
	// tests); at supervisor level that must leave no retry state behind.
	id := seedSession(t, nil)

	var calls [][2]any
	superviseSpawn = scriptSpawn([]int{recovery.ExitTerminal}, &calls)
	if got := superviseSessionCommand(context.Background(), id, 0, sessionTestBudget(), false); got != recovery.ExitTerminal {
		t.Errorf("superviseSessionCommand = %d, want %d", got, recovery.ExitTerminal)
	}
	raw, err := os.ReadFile(filepath.Join(".forcefield", "sessions", id+".json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "supervisor") {
		t.Errorf("terminal outcome wrote supervisor state:\n%s", raw)
	}
}

func TestSuperviseMissingSessionDegradesToMemoryBudget(t *testing.T) {
	isolateSuperviseGlobals(t)
	enterSuperviseTempDir(t)

	// No session file: explicit bounded in-memory behavior instead of a
	// refusal — the child fails closed on its own for a bad id.
	var calls [][2]any
	superviseSpawn = scriptSpawn([]int{3, 3}, &calls)
	budget := recovery.Budget{MaxRestarts: 1, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	if got := superviseSessionCommand(context.Background(), "ghost-session", 0, budget, false); got != recovery.ExitRetryable {
		t.Errorf("superviseSessionCommand = %d, want %d", got, recovery.ExitRetryable)
	}
	if len(calls) != 2 {
		t.Errorf("attempts = %d, want 1 initial + 1 restart", len(calls))
	}
}

// TestSuperviseConcurrentInvocations documents the adversarial case:
// two supervisors on one session cannot corrupt it (atomic saves, valid
// file, conversation intact), though they may spend overlapping budgets
// — preventing that needs locking the architecture does not have.
func TestSuperviseConcurrentInvocations(t *testing.T) {
	isolateSuperviseGlobals(t)
	enterSuperviseTempDir(t)
	id := seedSession(t, nil)

	superviseSpawn = func(context.Context, string, int) (int, error) {
		time.Sleep(50 * time.Millisecond)
		return recovery.ExitOK, nil
	}
	var wg sync.WaitGroup
	results := make([]int, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = superviseSessionCommand(context.Background(), id, 0, sessionTestBudget(), false)
		}(i)
	}
	wg.Wait()
	for _, code := range results {
		if code != 0 {
			t.Errorf("concurrent supervise = %d, want 0", code)
		}
	}
	final := loadSupervisor(t, id)
	if len(final.Messages) != 1 || final.Messages[0].Content != "supervised work" {
		t.Errorf("conversation damaged by concurrent supervisors: %+v", final.Messages)
	}
	if final.Supervisor != nil {
		t.Errorf("Supervisor = %+v, want nil after successes", final.Supervisor)
	}
}

// TestSuperviseTwoEpisodeLifecycle drives the full wrapper-visible
// lifecycle through one session file: an episode exhausts and latches,
// a fresh external invocation refuses without spending, an explicit
// reset starts a new episode, and its success leaves no residue.
func TestSuperviseTwoEpisodeLifecycle(t *testing.T) {
	isolateSuperviseGlobals(t)
	enterSuperviseTempDir(t)
	id := seedSession(t, nil)
	budget := recovery.Budget{MaxRestarts: 1, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}

	// Episode 1: one retryable failure spends the single restart, the
	// next failure exhausts and latches.
	var calls [][2]any
	superviseSpawn = scriptSpawn([]int{3, 3}, &calls)
	if got := superviseSessionCommand(context.Background(), id, 0, budget, false); got != recovery.ExitRetryable {
		t.Fatalf("episode 1 = %d, want %d", got, recovery.ExitRetryable)
	}
	if len(calls) != 2 {
		t.Fatalf("episode 1 attempts = %d, want 2", len(calls))
	}
	st := loadSupervisor(t, id).Supervisor
	if st == nil || st.ExhaustedAt == 0 {
		t.Fatalf("Supervisor = %+v, want a latched episode", st)
	}

	// A fresh external invocation (new supervisor process) refuses
	// without spawning or spending: the pathological wrapper loop is
	// reduced to a cheap refusal.
	calls = nil
	if got := superviseSessionCommand(context.Background(), id, 0, budget, false); got != recovery.ExitTerminal {
		t.Fatalf("refusal = %d, want %d", got, recovery.ExitTerminal)
	}
	if len(calls) != 0 {
		t.Fatalf("refusal spawned %d children, want none", len(calls))
	}

	// Explicit reset starts a new episode; its success clears everything.
	superviseSpawn = scriptSpawn([]int{0}, &calls)
	if got := superviseSessionCommand(context.Background(), id, 0, budget, true); got != 0 {
		t.Fatalf("reset episode = %d, want 0", got)
	}
	if len(calls) != 1 {
		t.Fatalf("reset attempts = %d, want 1", len(calls))
	}
	if st := loadSupervisor(t, id).Supervisor; st != nil {
		t.Errorf("Supervisor = %+v, want nil after the reset episode succeeds", st)
	}
}

// TestMirrorSuperviseEventSaveFailureDoesNotAdvance pins P1 Test B: when
// the restart mirror write fails, the error is returned (never
// swallowed) and the file does not advance as though the write
// succeeded. The session seeds healthy (writes worked), then the write
// path breaks — the late-onset 5-day failure mode — so the mirror must
// report the failure while the persisted budget stays at its last good
// value. (In-memory caller mutations predate the save and are not
// rolled back; containment comes from the caller stopping supervision
// on this error, pinned by TestDriveSupervisionStopsWhenMirrorFails.)
func TestMirrorSuperviseEventSaveFailureDoesNotAdvance(t *testing.T) {
	enterSuperviseTempDir(t)
	id := seedSession(t, &session.SupervisorState{Restarts: 1})
	sess := loadSupervisor(t, id)
	// Break the write path after the healthy seed: an ID the saver
	// always rejects makes every subsequent Save fail deterministically.
	sess.ID = "bad/id"
	err := mirrorSuperviseEvent(sess, id, 3, recovery.SuperviseEvent{Attempt: 3, Retry: true})
	if err == nil {
		t.Fatal("mirrorSuperviseEvent = nil, want the save failure surfaced")
	}
	if !strings.Contains(err.Error(), "record restart") {
		t.Errorf("err = %v, want it to name the failed mirror write", err)
	}
	if sess.LastSaveError == "" {
		t.Error("LastSaveError empty, want the sticky save error recorded")
	}
	// The file keeps its last good budget: a later supervisor resumes
	// from restarts 1, not the unpersisted 3.
	if st := loadSupervisor(t, id).Supervisor; st == nil || st.Restarts != 1 {
		t.Errorf("file Supervisor = %+v, want restarts 1 (no advance on failed write)", st)
	}
}

// TestMirrorSuperviseEventSuccessPersists pins P1 Test C for the
// supervisor path: a healthy mirror write records the count, clears
// any error, and survives a reload.
func TestMirrorSuperviseEventSuccessPersists(t *testing.T) {
	enterSuperviseTempDir(t)
	sess := session.New()
	sess.AddMessage("user", "supervised work")
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := mirrorSuperviseEvent(sess, sess.ID, 2, recovery.SuperviseEvent{Attempt: 2, Retry: true}); err != nil {
		t.Fatalf("mirrorSuperviseEvent = %v, want nil on a healthy write", err)
	}
	if sess.LastSaveError != "" {
		t.Errorf("LastSaveError = %q, want clean after success", sess.LastSaveError)
	}
	if st := loadSupervisor(t, sess.ID).Supervisor; st == nil || st.Restarts != 2 {
		t.Errorf("file Supervisor = %+v, want restarts 2", st)
	}
}

// TestPersistSuperviseEventSuccessPaths pins that the verified-write
// wrapper preserves existing behavior on healthy storage across the
// whole episode arc: retry records, exhaustion latches, terminal
// outcome clears.
func TestPersistSuperviseEventSuccessPaths(t *testing.T) {
	enterSuperviseTempDir(t)
	id := seedSession(t, nil)
	retry := recovery.SuperviseEvent{Attempt: 1, Retry: true}
	if err := persistSuperviseEvent(id, 0, retry); err != nil {
		t.Fatalf("retry mirror = %v, want nil", err)
	}
	if st := loadSupervisor(t, id).Supervisor; st == nil || st.Restarts != 1 {
		t.Fatalf("file Supervisor = %+v, want restarts 1", st)
	}
	if err := persistSuperviseEvent(id, 1, recovery.SuperviseEvent{Attempt: 2, Exhausted: true, Final: true}); err != nil {
		t.Fatalf("exhaust mirror = %v, want nil", err)
	}
	if st := loadSupervisor(t, id).Supervisor; st == nil || st.ExhaustedAt == 0 {
		t.Fatalf("file Supervisor = %+v, want a latched episode", st)
	}
	done := recovery.SuperviseEvent{Attempt: 3, Code: recovery.ExitOK, Final: true}
	if err := persistSuperviseEvent(id, 1, done); err != nil {
		t.Fatalf("clear mirror = %v, want nil", err)
	}
	if st := loadSupervisor(t, id).Supervisor; st != nil {
		t.Errorf("file Supervisor = %+v, want nil after terminal clear", st)
	}
}

// TestDriveSupervisionStopsWhenMirrorFails pins the fail-closed
// consequence: when the restart mirror cannot be verified (the emit
// layer reports the failure and cancels, exactly as
// superviseSessionCommand does), supervision parks after the single
// spent attempt instead of silently consuming further budget.
func TestDriveSupervisionStopsWhenMirrorFails(t *testing.T) {
	isolateSuperviseGlobals(t)
	var calls [][2]any
	superviseSpawn = scriptSpawn([]int{3, 3, 3}, &calls)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emit := func(e recovery.SuperviseEvent) {
		if e.Retry {
			// Mirror write failed: stop spending restarts.
			cancel()
		}
	}
	got := driveSupervision(ctx, "sess-mirror-fail", 0, sessionTestBudget(), 0, emit)
	if got != recovery.ExitNeedsHuman {
		t.Errorf("driveSupervision = %d, want %d (parked after mirror failure)", got, recovery.ExitNeedsHuman)
	}
	if len(calls) != 1 {
		t.Errorf("spawned %d children, want 1 (no restart after an unverified mirror write)", len(calls))
	}
}
