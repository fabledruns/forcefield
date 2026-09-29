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
	// ExitUnverified is a run the model finished (EventDone) without
	// establishing verification: tools ran but nothing recorded a
	// passing verification, so the final status is partial (or
	// blocked/failed). The output may still be printed, but the
	// operating system must not report success. Needs human review;
	// never auto-restarted.
	ExitUnverified = 5
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

// Classify maps a terminal runtime outcome to an exit code, reusing
// providers.IsTransient (no second retryability system). See
// docs/Recovery.md for precedence. Quota/billing, auth, invalid, and
// protocol errors are never transient.
//
// The status parameter carries the EventDone verification outcome: only
// StatusVerified completes with ExitOK. Any other Done status (partial,
// blocked, failed, or empty from a hand-built event) completes
// unverified. Plain chat is unaffected: with zero tool calls FinalStatus
// reports verified, so ordinary answers keep exiting 0.
func Classify(eventType runtime.EventType, err error, stats Stats, status runtime.Status) int {
	if eventType == runtime.EventDone {
		if status == runtime.StatusVerified {
			return ExitOK
		}
		return ExitUnverified
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
