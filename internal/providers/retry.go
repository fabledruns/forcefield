package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// This file is the single place where provider HTTP requests are retried
// and rate limits are honored. Both providers route their
// connection/status phase through doWithRetry so the policy lives in one
// place while each provider keeps its own request building and stream
// parsing.

// retryPolicy bounds how a provider's HTTP boundary retries transient
// rate-limit responses. Every dimension is capped so a misbehaving server
// can never make the agent retry forever or wait unreasonably long.
type retryPolicy struct {
	// MaxRetries is how many additional attempts a retryable 429 gets on
	// top of the first attempt.
	MaxRetries int
	// BaseBackoff is the delay before the first retry when the response
	// carries no Retry-After; each subsequent retry doubles it.
	BaseBackoff time.Duration
	// MaxBackoff caps the computed exponential backoff.
	MaxBackoff time.Duration
	// MaxRetryAfter caps how long a server-provided Retry-After is
	// honored; a hint larger than this is clamped rather than followed.
	MaxRetryAfter time.Duration
}

// defaultRetryPolicy tolerates brief rate-limit windows (a handful of
// retries spanning a few seconds) without letting a sustained 429 stall a
// run indefinitely.
var defaultRetryPolicy = retryPolicy{
	MaxRetries:    3,
	BaseBackoff:   time.Second,
	MaxBackoff:    30 * time.Second,
	MaxRetryAfter: 60 * time.Second,
}

// statusError reports a non-200 response from a provider after any
// permitted retries ran out (or never applied).
type statusError struct {
	Provider     string
	Model        string
	Status       int
	Body         string
	Kind         ErrorKind     // normalized classification of Status
	RetryAfter   time.Duration // server hint, 0 when absent
	Retries      int           // retries performed before giving up
	RateLimited  bool
	NonRetryable bool // quota/billing exhaustion: waiting cannot help
	// Hint, when non-empty, is a user-facing suggestion for what to do
	// next (e.g. "run ollama pull …"). Providers attach it after the
	// fact based on status code and their own configuration.
	Hint string
}

func (e *statusError) Error() string {
	msg := fmt.Sprintf("%s returned status %d for model %q: %s",
		e.Provider, e.Status, e.Model, strings.TrimSpace(e.Body))
	switch {
	case e.NonRetryable:
		msg += " (quota or billing limit reached; not retryable - check the provider account for this API key)"
	case e.Retries > 0:
		msg += fmt.Sprintf(" (rate limited; gave up after %d retries)", e.Retries)
	}
	if e.Hint != "" {
		msg += "\n" + e.Hint
	}
	return msg
}

// parseRetryAfter interprets a Retry-After header value - delta-seconds or
// HTTP-date - as a duration from now. Negative values clamp to zero.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := at.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

// quotaPhrases mark 429 bodies that mean account exhaustion rather than a
// transient rate limit. Exhaustion is never retried: no amount of waiting
// fixes it, and the user has to act on the account instead.
var quotaPhrases = []string{
	"quota",
	"billing",
	"credit",
	"payment required",
	"upgrade your",
	"subscription",
}

// quotaExhausted reports whether a 429 body indicates quota/billing
// exhaustion (non-retryable) instead of a transient rate limit.
func quotaExhausted(body string) bool {
	lower := strings.ToLower(body)
	for _, phrase := range quotaPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// backoffDelay computes the wait before retry N: exponential in the retry
// index, capped at MaxBackoff, with equal jitter (half fixed, half random)
// so multiple agents hitting the same limit don't retry in lockstep.
func backoffDelay(p retryPolicy, retry int) time.Duration {
	delay := p.BaseBackoff
	for i := 0; i < retry && delay < p.MaxBackoff; i++ {
		delay *= 2
	}
	if delay > p.MaxBackoff {
		delay = p.MaxBackoff
	}
	half := delay / 2
	if half <= 0 {
		return delay
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// maxErrorBodyBytes bounds how much of an error response body is read into
// memory and error messages; API error payloads are tiny.
const maxErrorBodyBytes = 8 * 1024

// doWithRetry performs one provider HTTP request, retrying transient 429
// responses according to policy. buildRequest runs once per attempt
// because request bodies are single-use. Only the connection/status phase
// is covered: once a 200 body is handed back, mid-stream errors stay with
// the caller, since replaying a partially consumed stream would duplicate
// output. Transport-level errors are fatal, wrapped by wrapTransport when
// provided, matching the pre-retry behavior of both providers.
func doWithRetry(
	ctx context.Context,
	client *http.Client,
	p retryPolicy,
	provider, model string,
	buildRequest func() (*http.Request, error),
	wrapTransport func(error) error,
) (*http.Response, error) {
	for retry := 0; ; retry++ {
		req, err := buildRequest()
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		if err != nil {
			if wrapTransport != nil {
				err = wrapTransport(err)
			}
			return nil, err
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()

		hint, hasHint := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())

		statusErr := &statusError{
			Provider:   provider,
			Model:      model,
			Status:     resp.StatusCode,
			Body:       string(body),
			Kind:       statusForKind(resp.StatusCode),
			RetryAfter: hint,
			Retries:    retry,
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			return nil, statusErr
		}
		statusErr.RateLimited = true

		// Quota/billing exhaustion looks like a 429 but waiting can't fix
		// it; fail immediately instead of burning the attempt budget.
		if quotaExhausted(string(body)) {
			statusErr.NonRetryable = true
			statusErr.Kind = ErrKindQuota
			return nil, statusErr
		}

		if retry >= p.MaxRetries {
			return nil, statusErr
		}

		delay := backoffDelay(p, retry)
		if hasHint {
			delay = hint
			if delay > p.MaxRetryAfter {
				delay = p.MaxRetryAfter
			}
		}

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// errRequestInFlight is returned when a second inference request hits a
// provider while one is still streaming. The agent loop is sequential by
// design, so this always indicates a bug worth surfacing, not queueing.
var errRequestInFlight = errors.New("another inference request is already in flight for this provider")

// annotateStatusHint attaches a provider-specific action hint to a
// returned *statusError (found via errors.As) and returns it; any other
// error passes through unchanged. Keeping the classification at the call
// site lets each provider speak for itself without doWithRetry needing to
// know which service is on the other end of the wire.
func annotateStatusHint(err error, hintFor func(status int, body string) string) error {
	var se *statusError
	if !errors.As(err, &se) {
		return err
	}
	if h := hintFor(se.Status, se.Body); h != "" {
		se.Hint = h
	}
	return se
}

// requestGate enforces one in-flight inference request per provider
// instance, structurally preventing accidental duplicate concurrent
// requests that providers would rate-limit as a burst.
type requestGate struct{ sem chan struct{} }

func newRequestGate() *requestGate {
	return &requestGate{sem: make(chan struct{}, 1)}
}

func (g *requestGate) acquire() error {
	select {
	case g.sem <- struct{}{}:
		return nil
	default:
		return errRequestInFlight
	}
}

func (g *requestGate) release() {
	select {
	case <-g.sem:
	default:
	}
}
