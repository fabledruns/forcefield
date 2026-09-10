package recovery

import (
	"context"
	"time"
)

// ChildFunc runs one supervised attempt and reports the child process
// exit code. A non-nil error means the attempt never produced an exit
// code (spawn failure, wait failure, signaled without one): the
// supervisor treats that as an unprovable outcome and fails closed.
type ChildFunc func(ctx context.Context) (exitCode int, err error)

// SleepFunc waits between attempts. It receives the context so a wait
// can be abandoned when supervision is cancelled.
type SleepFunc func(ctx context.Context, d time.Duration)

// SleepContext is the production SleepFunc: a bounded wait that returns
// early when the context is cancelled.
func SleepContext(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// SuperviseEvent describes one supervision step for lifecycle reporting.
// Exactly one event carries Final=true; it ends supervision and its
// Code is the supervisor's exit code.
type SuperviseEvent struct {
	// Attempt is the 1-based child attempt number this event belongs to.
	Attempt int
	// Code is the child exit code, when the child finished.
	Code int
	// Err carries a spawn/wait failure. Set only on Final events that
	// stop supervision because the outcome is unprovable.
	Err error
	// Wait is the backoff before the next attempt. Set only on retry
	// events (Retry=true, Final=false).
	Wait time.Duration
	// Retry means another attempt follows after Wait.
	Retry bool
	// Exhausted means the restart budget is spent: supervision stops
	// with the last exit code (ExitRetryable) without another attempt.
	Exhausted bool
	// Final marks the event that ends supervision.
	Final bool
}

// Supervise runs child attempts until one parks: success (0), terminal
// failure (2), or cancellation/denial (4) stop immediately, as does any
// spawn/wait failure or unrecognized exit code (fail closed — an unknown
// outcome is never auto-retried). Only ExitRetryable (3) restarts, while
// the budget allows; an exhausted budget stops with the last code (3).
//
// Supervise is deliberately dumb and stateless: it owns the attempt
// counter and nothing else. The session file owns run state, the child
// owns execution, and the budget owns the restart policy. It never
// touches sessions, providers, or tools. emit may be nil.
//
// Exit codes returned are the Phase 0 contract codes (0/2/3/4), except
// 1 for supervisor-level failure (nil child func, spawn/wait error,
// unknown child code, cancelled wait). Code 1 is never a run outcome
// and must never be interpreted as retryable.
func Supervise(ctx context.Context, budget Budget, child ChildFunc, sleep SleepFunc, emit func(SuperviseEvent)) int {
	return SuperviseFrom(ctx, budget, 0, child, sleep, emit)
}

// SuperviseFrom is Supervise resuming with used restarts already spent
// in the current episode (read from persisted supervisor lifecycle
// state). Backoff indexing continues from used, so a supervisor killed
// during backoff resumes with its remaining budget instead of a fresh
// one. A negative used counts as zero.
func SuperviseFrom(ctx context.Context, budget Budget, used int, child ChildFunc, sleep SleepFunc, emit func(SuperviseEvent)) int {
	if child == nil {
		return 1
	}
	if sleep == nil {
		sleep = SleepContext
	}
	if emit == nil {
		emit = func(SuperviseEvent) {}
	}
	restarts := used
	if restarts < 0 {
		restarts = 0
	}
	for attempt := 1; ; attempt++ {
		code, err := child(ctx)
		if err != nil {
			// The attempt never produced an exit code: retrying would
			// be guessing, so park for a human instead.
			emit(SuperviseEvent{Attempt: attempt, Err: err, Final: true})
			return 1
		}
		switch code {
		case ExitOK, ExitTerminal, ExitNeedsHuman:
			emit(SuperviseEvent{Attempt: attempt, Code: code, Final: true})
			return code
		case ExitRetryable:
			if !budget.Allow(restarts) {
				emit(SuperviseEvent{Attempt: attempt, Code: code, Exhausted: true, Final: true})
				return code
			}
			wait := budget.Backoff(restarts)
			restarts++
			emit(SuperviseEvent{Attempt: attempt, Code: code, Wait: wait, Retry: true})
			sleep(ctx, wait)
			if ctx.Err() != nil {
				// Cancelled while waiting: the child may have reported
				// retryable, but supervision itself was torn down, so
				// park as needs-human rather than restarting into a
				// cancelled context.
				emit(SuperviseEvent{Attempt: attempt, Code: ExitNeedsHuman, Final: true})
				return ExitNeedsHuman
			}
		default:
			// Unrecognized child code (e.g. 1 = child setup failure):
			// fail closed rather than turning it into recovery.
			emit(SuperviseEvent{Attempt: attempt, Code: code, Final: true})
			return 1
		}
	}
}
