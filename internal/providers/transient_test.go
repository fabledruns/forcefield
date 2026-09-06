package providers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestIsTransient(t *testing.T) {
	timeoutErr := &url.Error{Op: "Post", URL: "http://x", Err: context.DeadlineExceeded}
	resetErr := &url.Error{Op: "Post", URL: "http://x", Err: &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}}
	dnsErr := &net.DNSError{Err: "no such host", IsNotFound: true}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429 rate limit", &statusError{Status: 429, Kind: ErrKindRateLimit}, true},
		{"429 quota exhaustion", &statusError{Status: 429, Kind: ErrKindQuota, NonRetryable: true}, false},
		{"402 payment required", &statusError{Status: 402, Kind: ErrKindQuota, NonRetryable: true}, false},
		{"500 server", &statusError{Status: 500, Kind: ErrKindServer}, true},
		{"503 server", &statusError{Status: 503, Kind: ErrKindServer}, true},
		{"501 not implemented", &statusError{Status: 501, Kind: ErrKindServer}, false},
		{"401 auth", &statusError{Status: 401, Kind: ErrKindAuth}, false},
		{"404 not found", &statusError{Status: 404, Kind: ErrKindNotFound}, false},
		{"400 invalid", &statusError{Status: 400, Kind: ErrKindInvalidRequest}, false},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"transport timeout", timeoutErr, true},
		{"connection reset", resetErr, true},
		{"dns failure", dnsErr, true},
		{"canceled", context.Canceled, false},
		{"protocol", &protocolError{msg: "bad chunk"}, false},
		{"plain error", errors.New("boom"), false},
		{"in-flight guard", errRequestInFlight, false},
	}
	for _, tc := range cases {
		if got := IsTransient(tc.err); got != tc.want {
			t.Errorf("IsTransient(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTurnRetryDelayBounds(t *testing.T) {
	// Plain backoff: bounded, non-negative, grows across attempts on
	// average (jitter makes strict monotonicity untestable; assert caps).
	for retry := 0; retry < 5; retry++ {
		d := TurnRetryDelay(retry, errors.New("boom"))
		if d < 0 || d > 30*time.Second {
			t.Errorf("TurnRetryDelay(%d) = %v, want within [0, 30s]", retry, d)
		}
	}
	// Short explicit Retry-After is honored exactly.
	short := &statusError{Status: http.StatusTooManyRequests, Kind: ErrKindRateLimit, RetryAfter: 2 * time.Second}
	if d := TurnRetryDelay(0, short); d != 2*time.Second {
		t.Errorf("short Retry-After delay = %v, want exactly 2s", d)
	}
	// A huge Retry-After must not stall the turn: fall back to backoff.
	huge := &statusError{Status: http.StatusTooManyRequests, Kind: ErrKindRateLimit, RetryAfter: 10 * time.Minute}
	if d := TurnRetryDelay(0, huge); d <= 0 || d > 30*time.Second {
		t.Errorf("huge Retry-After delay = %v, want bounded backoff instead", d)
	}
	// Zero Retry-After behaves like no hint.
	none := &statusError{Status: 500, Kind: ErrKindServer}
	if d := TurnRetryDelay(0, none); d <= 0 || d > 30*time.Second {
		t.Errorf("no-hint delay = %v, want bounded backoff", d)
	}
}
