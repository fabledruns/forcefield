package recovery

import (
	"time"

	"forcefield/internal/session"
)

// Supervisor lifecycle persistence (see session.SupervisorState and
// docs/Recovery.md). The in-memory loop stays exact; these helpers mirror
// its counter into the session file so the budget survives a supervisor
// kill. Values are set absolutely so replay converges. No locking:
// concurrent supervisors must not share a session (see docs).
func NoteSupervisorRestart(sess *session.Session, restarts int) {
	if sess == nil {
		return
	}
	if restarts < 0 {
		restarts = 0
	}
	if sess.Supervisor == nil {
		sess.Supervisor = &session.SupervisorState{}
	}
	sess.Supervisor.Restarts = restarts
	_ = sess.Save()
}

// NoteSupervisorExhausted latches the current episode as spent at the
// current UTC time. Supervised restarts for this session are refused
// until the latch clears. Idempotent.
func NoteSupervisorExhausted(sess *session.Session) {
	if sess == nil {
		return
	}
	if sess.Supervisor == nil {
		sess.Supervisor = &session.SupervisorState{}
	}
	sess.Supervisor.ExhaustedAt = time.Now().UTC().Unix()
	_ = sess.Save()
}

// ClearSupervisor drops any supervised-restart lifecycle state,
// reporting whether anything changed (and only then saving, so clean
// sessions are never rewritten by lifecycle bookkeeping).
func ClearSupervisor(sess *session.Session) bool {
	if sess == nil || sess.Supervisor == nil {
		return false
	}
	sess.Supervisor = nil
	_ = sess.Save()
	return true
}
