package cmd

import (
	"testing"

	"forcefield/internal/recovery"
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
