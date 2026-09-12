package cmd

import (
	"errors"
	"strings"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/recovery"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// TestSettleSupervisorEpisode pins which headless outcomes close a
// supervised-restart episode: terminal outcomes (including quota/auth,
// which classify as terminal) clear lifecycle state, while retryable
// interruptions and setup failures keep it for a supervisor to continue.
func TestSettleSupervisorEpisode(t *testing.T) {
	enterSuperviseTempDir(t)

	clears := map[int]bool{
		recovery.ExitOK:         true,
		recovery.ExitTerminal:   true,
		recovery.ExitNeedsHuman: true,
		recovery.ExitRetryable:  false,
		1:                       false,
		99:                      false,
	}
	for code, wantClear := range clears {
		id := seedSession(t, &session.SupervisorState{Restarts: 2})
		settleSupervisorEpisode(mustLoadSupervisorSession(t, id), code)
		st := loadSupervisor(t, id).Supervisor
		if wantClear && st != nil {
			t.Errorf("code %d: Supervisor = %+v, want nil (episode closed)", code, st)
		}
		if !wantClear && (st == nil || st.Restarts != 2) {
			t.Errorf("code %d: Supervisor = %+v, want restarts 2 kept", code, st)
		}
	}
}

func mustLoadSupervisorSession(t *testing.T, id string) *session.Session {
	t.Helper()
	sess, err := session.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return sess
}

// A failed final save turns even an otherwise successful run terminal
// without clearing supervisor lifecycle, so a supervisor retries
// instead of assuming the record reached disk.
func TestFinishResumeSessionSaveFailureIsTerminalKeepsSupervisor(t *testing.T) {
	enterSuperviseTempDir(t)
	id := seedSession(t, &session.SupervisorState{Restarts: 2})
	sess := mustLoadSupervisorSession(t, id)
	sess.LastSaveError = "replace session file x after 10 attempts: access denied"

	driver := recovery.NewDriver(nil)
	driver.HandleEvent(runtime.Event{Type: runtime.EventDone, Response: &providers.Response{Content: "done"}})

	code, err := finishResumeSession(sess, driver, id, recovery.ExitOK)
	if code != recovery.ExitTerminal {
		t.Errorf("code = %d, want %d (failed final save)", code, recovery.ExitTerminal)
	}
	if err == nil || !strings.Contains(err.Error(), "session save failed") {
		t.Errorf("err = %v, want it to name the session save failure", err)
	}
	if sess.Supervisor == nil || sess.Supervisor.Restarts != 2 {
		t.Errorf("Supervisor = %+v, want restarts 2 kept (no clearing on save failure)", sess.Supervisor)
	}
	if st := loadSupervisor(t, id).Supervisor; st == nil || st.Restarts != 2 {
		t.Errorf("file Supervisor = %+v, want restarts 2 intact", st)
	}
}

// A clean final save preserves the existing settle/report behavior:
// success clears the episode and reports nil, and no save warning
// appears anywhere in the outcome.
func TestFinishResumeSessionCleanSavePreservesBehavior(t *testing.T) {
	enterSuperviseTempDir(t)
	id := seedSession(t, &session.SupervisorState{Restarts: 2})
	sess := mustLoadSupervisorSession(t, id)

	driver := recovery.NewDriver(nil)
	driver.HandleEvent(runtime.Event{Type: runtime.EventDone})

	code, err := finishResumeSession(sess, driver, id, recovery.ExitOK)
	if code != recovery.ExitOK || err != nil {
		t.Errorf("finish = (%d, %v), want (0, nil) on a clean save", code, err)
	}
	if st := loadSupervisor(t, id).Supervisor; st != nil {
		t.Errorf("Supervisor = %+v, want nil (clean success still clears)", st)
	}

	// A non-zero outcome without a save failure reports the run outcome
	// only — never a save warning.
	blocked := recovery.NewDriver(nil)
	blocked.HandleEvent(runtime.Event{Type: runtime.EventBlocked, Err: errors.New("stopped after 60 iterations (maximum reached)")})
	sess2 := mustLoadSupervisorSession(t, id)
	code, err = finishResumeSession(sess2, blocked, id, recovery.ExitTerminal)
	if code != recovery.ExitTerminal || err == nil {
		t.Fatalf("finish = (%d, %v), want (2, error)", code, err)
	}
	if strings.Contains(err.Error(), "session save failed") {
		t.Errorf("err = %v, must not mention save failure when the save succeeded", err)
	}
}
