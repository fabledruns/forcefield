package providers

import (
	"fmt"
	"net/http"
	"time"
)

// defaultStreamTimeout bounds how long the client will wait for the
// first response headers. It does not bound the entire streamed body.
// For streaming completions the headers should arrive quickly (even if the
// model then thinks for a long time), so we bound only the header phase
// and let the body stream for as long as the provider is actively sending
// SSE. Without a header bound a dead cloud endpoint would hang until the
// OS TCP timeout (minutes to hours).
const defaultStreamTimeout = 120 * time.Second

// Provider timeout bounds for per-provider timeout_seconds overrides.
// Zero means "use the default"; explicit values must fall in this range
// so a typo cannot disable the header bound or stall a run for hours.
const (
	minProviderTimeoutSeconds = 1
	maxProviderTimeoutSeconds = 300
)

func newDefaultClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: defaultStreamTimeout,
		},
		CheckRedirect: safeRedirectCheck,
	}
}

// clientForTimeout returns a client identical to newDefaultClient except
// the response-header timeout is overridden. Non-positive values keep the
// default so callers can pass through an unset timeout_seconds directly.
func clientForTimeout(timeoutSeconds int) *http.Client {
	timeout := defaultStreamTimeout
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}
	return &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: safeRedirectCheck,
	}
}

// safeRedirectCheck bounds endpoint redirects so custom providers cannot
// silently forward credentials to an unintended host. Same-host redirects
// (trailing slashes, version prefixes) keep working; cross-host redirects
// fail instead of forwarding Authorization or custom auth headers.
// Downgrades from https to http fail unless the target is loopback, where
// local inference servers legitimately serve plain http.
func safeRedirectCheck(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return fmt.Errorf("too many redirects (max 3)")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("redirect to non-http(s) URL %q is not allowed", req.URL.String())
	}
	if len(via) == 0 {
		return nil
	}
	first := via[0]
	if !equalRedirectHost(first.URL.Hostname(), req.URL.Hostname()) {
		return fmt.Errorf("cross-host redirect from %q to %q is not allowed", first.URL.Host, req.URL.Host)
	}
	if first.URL.Scheme == "https" && req.URL.Scheme == "http" && !isLoopbackHostname(req.URL.Hostname()) {
		return fmt.Errorf("downgrade from https to http for non-loopback host %q is not allowed", req.URL.Host)
	}
	return nil
}

func equalRedirectHost(a, b string) bool {
	if len(a) != len(b) {
		// Compare case-insensitively without allocating in the common
		// exact-match case.
		if len(a) == 0 || len(b) == 0 {
			return false
		}
	}
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ca, cb := a[ai], b[bi]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
		ai++
		bi++
	}
	return ai == len(a) && bi == len(b)
}
