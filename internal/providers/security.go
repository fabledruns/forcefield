package providers

import (
	"fmt"
)

// Header-value and credential validation for Phase 0 security follow-up.
// API keys resolve from env/.env (operator-controlled but easy to corrupt
// with a stray newline) and are interpolated into HTTP headers. A key or
// custom header value containing CR, LF, or other C0 controls would either
// split the header (injection) or fail obscurely at the transport layer.
// Validate before sending and fail closed without echoing the value.

// IsValidHeaderValue reports whether v is safe to place in an HTTP header.
// It rejects CR, LF, and other C0 controls (except HT, which RFC 7230
// permits in field values) plus DEL. Spaces and printable UTF-8 pass, so
// legitimate values like "Bearer ..." or referer URLs keep working.
func IsValidHeaderValue(v string) bool {
	for _, r := range v {
		switch {
		case r == '\t':
			continue
		case r < 0x20 || r == 0x7f:
			return false
		}
	}
	return true
}

// IsValidAPIKey reports whether key is safe to send as an HTTP header
// value. Empty means "no credential" and is valid; anything else must be
// a control-free header value.
func IsValidAPIKey(key string) bool {
	if key == "" {
		return true
	}
	return IsValidHeaderValue(key)
}

// invalidAPIKeyError builds a redaction-safe error for a credential that
// cannot be sent. It names the provider and, when known, the variable the
// value came from — never the value itself.
func invalidAPIKeyError(providerID, envVar string) error {
	if envVar != "" {
		return fmt.Errorf("provider %q: value in %s contains invalid control characters (check for stray line breaks) and will not be sent", providerID, envVar)
	}
	return fmt.Errorf("provider %q: API key contains invalid control characters (check for stray line breaks) and will not be sent", providerID)
}

// metadataBlocked reports whether baseURL targets a cloud metadata service
// without an explicit opt-in. Loopback and private LAN addresses are NOT
// metadata and are always allowed here; only the metadata endpoints that
// would echo cloud credentials are gated.
func metadataBlocked(baseURL string, allow bool) bool {
	if allow {
		return false
	}
	return IsMetadataHost(EndpointHost(baseURL))
}
