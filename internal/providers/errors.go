package providers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ErrorKind classifies a provider failure so callers (doctor, TUI, tests)
// can react without string matching. Adapters attach the most specific
// kind they can; Classify derives one from any error.
type ErrorKind int

const (
	ErrKindUnknown ErrorKind = iota
	ErrKindAuth
	ErrKindNotFound
	ErrKindRateLimit
	ErrKindQuota
	ErrKindServer
	ErrKindInvalidRequest
	ErrKindConnection
	ErrKindTimeout
	ErrKindCanceled
	ErrKindProtocol // malformed response body or stream chunk
	ErrKindUnsupportedCapability
)

// String renders the kind as the short user-facing phrase used in error
// summaries ("OpenAI request failed: authentication rejected").
func (k ErrorKind) String() string {
	switch k {
	case ErrKindAuth:
		return "authentication rejected"
	case ErrKindNotFound:
		return "not found"
	case ErrKindRateLimit:
		return "rate limited"
	case ErrKindQuota:
		return "quota exhausted"
	case ErrKindServer:
		return "provider server error"
	case ErrKindInvalidRequest:
		return "invalid request"
	case ErrKindConnection:
		return "connection failed"
	case ErrKindTimeout:
		return "timed out"
	case ErrKindCanceled:
		return "canceled"
	case ErrKindProtocol:
		return "malformed provider response"
	case ErrKindUnsupportedCapability:
		return "capability not supported by this provider"
	default:
		return "request failed"
	}
}

// statusForKind maps an HTTP status to its normalized kind.
func statusForKind(status int) ErrorKind {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrKindAuth
	case status == http.StatusNotFound:
		return ErrKindNotFound
	case status == http.StatusTooManyRequests:
		return ErrKindRateLimit
	case status == http.StatusPaymentRequired:
		return ErrKindQuota
	case status >= 500:
		return ErrKindServer
	case status >= 400:
		return ErrKindInvalidRequest
	default:
		return ErrKindUnknown
	}
}

// Classify inspects err and reports the most specific failure kind it can
// determine: wrapped *statusError kinds win, then context errors, then
// transport-level timeouts and connection failures.
func Classify(err error) ErrorKind {
	if err == nil {
		return ErrKindUnknown
	}
	var se *statusError
	if errors.As(err, &se) {
		if se.Kind != ErrKindUnknown {
			return se.Kind
		}
		return statusForKind(se.Status)
	}
	var pe *protocolError
	if errors.As(err, &pe) {
		return ErrKindProtocol
	}
	if errors.Is(err, context.Canceled) {
		return ErrKindCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrKindTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrKindTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ErrKindConnection
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return ErrKindConnection
	}
	if errors.Is(err, errRequestInFlight) {
		return ErrKindUnknown
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return Classify(ue.Err)
	}
	return ErrKindUnknown
}

// redactSecrets replaces every occurrence of each secret in s with a
// placeholder. Some providers echo request credentials inside error
// bodies; this guarantees a leaked key never reaches the screen or logs.
func redactSecrets(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}

// Redacted wraps err so its message (and wrapped chain messages, via
// Unwrap) never contain any of the given secrets. Errors that contain no
// secret are returned unchanged.
func Redacted(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := redactSecrets(err.Error(), secrets...)
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }
