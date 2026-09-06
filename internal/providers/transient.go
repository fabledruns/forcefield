package providers

import (
	"errors"
	"net/http"
	"time"
)

// maxTurnRetryAfter caps a server-provided Retry-After honored between
// model turns. The transport already waited (and clamped) this hint while
// exhausting its own retries; the turn level only honors short explicit
// waits and otherwise falls back to bounded backoff, so a stale or huge
// hint can never stall a run.
const maxTurnRetryAfter = 10 * time.Second

// IsTransient reports whether err is a transient provider failure that a
// later turn may survive: rate limits, server errors, timeouts, and
// connection failures. It mirrors the transport retry policy exactly:
// quota/billing exhaustion, 501 Not Implemented, auth, invalid requests,
// cancellations, and protocol errors are never transient.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var se *statusError
	if errors.As(err, &se) {
		if se.NonRetryable {
			return false
		}
		if se.Status == http.StatusNotImplemented {
			return false
		}
	}
	switch Classify(err) {
	case ErrKindRateLimit, ErrKindServer, ErrKindTimeout, ErrKindConnection:
		return true
	default:
		return false
	}
}

// TurnRetryDelay returns how long to wait before model-turn attempt retry
// (0-based): a short explicit server Retry-After when present, otherwise
// the shared bounded exponential backoff with jitter. Either way the
// wait is capped, so sustained failures fail fast instead of stalling.
func TurnRetryDelay(retry int, err error) time.Duration {
	var se *statusError
	if errors.As(err, &se) {
		if hint := se.RetryAfter; hint > 0 && hint <= maxTurnRetryAfter {
			return hint
		}
	}
	return backoffDelay(defaultRetryPolicy, retry)
}
