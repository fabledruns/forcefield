package recovery

import "time"

// Budget bounds how often a future supervisor may restart an
// ExitRetryable run. It is intentionally dumb: a cap plus bounded
// exponential backoff. It performs no restarting itself, holds no
// state, and knows nothing about sessions — the supervisor owns the
// attempt counter, the session file owns the run state.
type Budget struct {
	// MaxRestarts caps supervised restarts for one run. Zero means no
	// automatic restarts; negative is normalized to zero.
	MaxRestarts int
	// BaseBackoff is the wait before the first restart. Non-positive
	// values fall back to DefaultBaseBackoff.
	BaseBackoff time.Duration
	// MaxBackoff caps the exponential growth. Non-positive values fall
	// back to DefaultMaxBackoff.
	MaxBackoff time.Duration
}

// Default backoff bounds for supervised restarts. Small enough to
// recover quickly from blips, capped so a flapping run cannot stall
// attention (or billing) for long between attempts.
const (
	DefaultBaseBackoff = 5 * time.Second
	DefaultMaxBackoff  = 5 * time.Minute
)

// Allow reports whether another restart fits the budget.
// used counts restarts already spent on this run.
func (b Budget) Allow(used int) bool {
	if used < 0 {
		used = 0
	}
	return used < b.MaxRestarts
}

// Backoff returns the wait before restart number attempt (0-based):
// exponential in the attempt index, capped at MaxBackoff. Deterministic
// by design — no jitter here; a supervisor fronting many runs can add
// its own decorrelation without this contract changing under it.
func (b Budget) Backoff(attempt int) time.Duration {
	base := b.BaseBackoff
	if base <= 0 {
		base = DefaultBaseBackoff
	}
	max := b.MaxBackoff
	if max <= 0 {
		max = DefaultMaxBackoff
	}
	if attempt < 0 {
		attempt = 0
	}
	delay := base
	for i := 0; i < attempt && delay < max; i++ {
		delay *= 2
		if delay < 0 { // overflow guard: saturate instead of wrapping
			delay = max
			break
		}
	}
	if delay > max {
		delay = max
	}
	return delay
}
