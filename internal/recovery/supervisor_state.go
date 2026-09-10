package recovery

import (
	"time"

	"forcefield/internal/session"
)

// Supervisor lifecycle persistence (see session.SupervisorState).
//
// The supervisor loop itself stays in-memory and exact; these helpers
// mirror its counter into the session file so the budget survives the
// supervisor process being killed and restarted:
//
//   - NoteSupervisorRestart records that restarts supervised attempts
//     have been spent in the current episode. Call it when a retry is
//     committed, before the backoff wait, so a kill during backoff
//     resumes with remaining budget.
//   - NoteSupervisorExhausted latches the episode as spent. A latched
//     episode refuses further supervised restarts until cleared.
//   - ClearSupervisor drops the episode state. Call it when a child
//     reaches a terminal outcome (0/2/4): the episode is over for a
//     reason other than retry-exhaustion, so a later episode starts
//     clean. Terminal failures therefore never accumulate retry state,
//     and neither do quota/auth failures (they classify to exit 2).
//
// All three are nil-session safe, no-ops when there is nothing to
// record, and persist through the existing atomic session Save. Values
// are set absolutely (not incremented) so replaying an event converges
// to the same state.
//
// Crash convergence: every helper runs synchronously between child
// exit and the next spawn. A kill anywhere in that window leaves either
// the pre- or post-write file, both valid: a missed increment spends at
// most one extra bounded restart; a missed latch is rewritten by the
// next exhaustion; a missed clear is harmless because terminal outcomes
// are re-observed (and re-cleared) idempotently.
//
// Concurrency warning: there is no locking. Concurrent supervisors
// racing read-modify-write may lose increments (bounded extra restarts
// — each invocation still enforces its own budget) and may interleave
// clear/set (last writer wins; a wrongly-set latch is recoverable via
// explicit reset or a successful manual run). Supervisor writes never
// touch Messages or Turn, so the conversation record cannot be
// corrupted by lifecycle bookkeeping — though concurrent supervisors
// still must not share a session, because their CHILDREN would corrupt
// each other through the pre-existing last-writer-wins session hazard.
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
