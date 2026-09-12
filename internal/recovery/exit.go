package recovery

import (
	"context"
	"errors"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// Exit codes: the Phase 0 recovery contract for resumable headless runs.
// A future supervisor may automatically restart ONLY ExitRetryable.
// Every other non-zero code parks for a human.
const (
	// ExitOK ran to EventDone.
	ExitOK = 0
	// ExitTerminal is a runtime-enforced stop (EventBlocked: limits,
	// loop detector, finish_length) or a non-transient failure
	// (auth, quota/billing, invalid request, protocol, unknown).
	// Retrying cannot help; a human must change something first.
	ExitTerminal = 2
	// ExitRetryable is a transient-class interruption (rate limit,
	// server 5xx, timeout, connection) that a fresh process may
	// survive with a new retry budget. Safe for supervised restart.
	ExitRetryable = 3
	// ExitNeedsHuman is caller cancellation or a run stalled on
	// permission denials: progress requires a human decision.
	// Never auto-restarted.
	ExitNeedsHuman = 4
)

// RetryableForSupervisor reports whether an exit code permits an
// automatic supervised restart. Only ExitRetryable qualifies:
// terminal failures need a fix, and cancellations/denials need a human.
func RetryableForSupervisor(code int) bool {
	return code == ExitRetryable
}

// Stats counts terminal tool outcomes observed during one driven run.
// It exists so the exit classification can tell "the agent worked and
// hit a wall" (terminal) from "nothing ran without a human saying so"
// (needs human) without parsing error strings.
type Stats struct {
	Done      int
	Failed    int
	Denied    int
	Cancelled int
}

// DeniedOnly reports whether every tool outcome this run was a denial:
// the agent made zero autonomous progress and is waiting on approvals.
func (s Stats) DeniedOnly() bool {
	return s.Denied > 0 && s.Done == 0 && s.Failed == 0
}

// Tally records one terminal tool event.
func (s *Stats) Tally(t runtime.EventType) {
	switch t {
	case runtime.EventToolFinish:
		s.Done++
	case runtime.EventToolFailed:
		s.Failed++
	case runtime.EventToolDenied:
		s.Denied++
	case runtime.EventToolCancelled:
		s.Cancelled++
	}
}

// Classify maps a terminal runtime outcome to an exit code. It reuses
// the existing provider classification (providers.IsTransient) — there
// is no second retryability system. Precedence:
//
//	Done → OK (even with earlier denials; the run finished).
//	Cancelled, or any context cancellation → NeedsHuman.
//	Blocked/Error with zero autonomous progress (denied-only) → NeedsHuman.
//	Blocked → Terminal.
//	Error of transient class → Retryable.
//	Anything else, including nil/unknown errors → Terminal (fail closed:
//	never auto-restart what cannot be proven retryable).
//
// Quota/billing, auth, invalid requests, and protocol errors are never
// transient per providers.IsTransient, so they land Terminal and can
// never become automatic restart loops.
func Classify(eventType runtime.EventType, err error, stats Stats) int {
	if eventType == runtime.EventDone {
		return ExitOK
	}
	if eventType == runtime.EventCancelled {
		return ExitNeedsHuman
	}
	if errors.Is(err, context.Canceled) {
		return ExitNeedsHuman
	}
	// A bare deadline (caller context timed out, never classified by the
	// runtime) needs a human decision. A runtime-marked transient timeout
	// carries a provider classification underneath, so it skips this
	// shortcut and is evaluated as transient below.
	if errors.Is(err, context.DeadlineExceeded) && !runtime.IsTransientError(err) {
		return ExitNeedsHuman
	}
	if eventType == runtime.EventBlocked || eventType == runtime.EventError {
		if stats.DeniedOnly() {
			return ExitNeedsHuman
		}
		if eventType == runtime.EventBlocked {
			return ExitTerminal
		}
		if providers.IsTransient(err) {
			return ExitRetryable
		}
		return ExitTerminal
	}
	return ExitTerminal
}
