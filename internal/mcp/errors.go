package mcp

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Typed errors for the MCP boundary. The surface is deliberately small:
// later phases map transport, handshake, discovery, and invocation
// failures onto these sentinels. Callers match with errors.Is and wrap
// with %w; human-facing strings are built with BoundedDetail so errors
// can never carry unbounded attacker-controlled payloads.
var (
	// ErrProtocol reports a JSON-RPC or MCP wire violation (wrong version
	// marker, batch payload, malformed envelope, unexpected server-
	// initiated request). The peer is not trusted to behave.
	ErrProtocol = errors.New("mcp: protocol error")

	// ErrInvalidMessage reports a message that is well-formed JSON but not
	// a usable JSON-RPC request, notification, or response.
	ErrInvalidMessage = errors.New("mcp: invalid message")

	// ErrUnsupportedVersion reports a negotiated protocol version outside
	// the explicit allowlist. Unknown and future versions fail closed.
	ErrUnsupportedVersion = errors.New("mcp: unsupported protocol version")

	// ErrTooLarge reports a protocol message or configured value that
	// exceeds its bound. The bound, not the payload, is the message.
	ErrTooLarge = errors.New("mcp: value too large")

	// ErrInvalidTool reports one malformed tool definition. Per the
	// skip-not-fail rule it skips that tool; it never fails a server.
	ErrInvalidTool = errors.New("mcp: invalid tool definition")

	// ErrMismatch reports a response whose identifier matches no pending
	// request. Late arrivals after cancel, timeout, crash, or shutdown
	// land here and are dropped without touching pending state.
	ErrMismatch = errors.New("mcp: request/response mismatch")

	// ErrInvalidConfig reports a malformed mcp.servers configuration
	// entry. Field paths name the key and field, never secret values.
	ErrInvalidConfig = errors.New("mcp: invalid server configuration")

	// ErrTransport reports a broken transport stream (I/O failure,
	// unexpected EOF, oversize frame). Pending requests fail with it and
	// later layers must not retry or respawn at this layer: the stream is
	// terminally unusable.
	ErrTransport = errors.New("mcp: transport failure")

	// ErrClosed reports use after shutdown. Shutdown is explicit and
	// idempotent; operations racing or following it fail fast with this
	// sentinel instead of touching the stream.
	ErrClosed = errors.New("mcp: transport closed")
)

// BoundedDetail truncates an attacker- or user-controlled excerpt to
// MaxErrorDetailRunes on a rune boundary for safe inclusion in errors.
// Empty input stays empty; short input is returned unchanged.
func BoundedDetail(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	if len(runes) <= MaxErrorDetailRunes {
		return s
	}
	return string(runes[:MaxErrorDetailRunes]) + TruncationMarker
}

// protocolErrorf builds a wrapped ErrProtocol with a bounded detail suffix.
// detail carries untrusted content (a method name, an excerpt); format
// carries only caller-controlled text.
func protocolErrorf(format string, args ...any) error {
	return fmt.Errorf("mcp: protocol error: %s: %w", fmt.Sprintf(format, args...), ErrProtocol)
}

// invalidMessagef builds a wrapped ErrInvalidMessage.
func invalidMessagef(format string, args ...any) error {
	return fmt.Errorf("mcp: invalid message: %s: %w", fmt.Sprintf(format, args...), ErrInvalidMessage)
}

// configErrorf builds a wrapped ErrInvalidConfig for servers.<key>.<field>
// paths. Values (commands aside) are never embedded; use BoundedDetail on
// the rare excerpt that must be shown.
func configErrorf(format string, args ...any) error {
	return fmt.Errorf("mcp: invalid server configuration: %s: %w", fmt.Sprintf(format, args...), ErrInvalidConfig)
}

// validUTF8Prefix cuts s to at most maxBytes on a rune boundary, mirroring
// the runtime's truncation discipline so markers never split a character.
func validUTF8Prefix(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := 0
	for i, r := range s {
		if i+utf8.RuneLen(r) > maxBytes {
			break
		}
		cut = i + utf8.RuneLen(r)
	}
	if cut == 0 {
		return ""
	}
	return s[:cut]
}

// quoteBounded renders an excerpt for diagnostics: trimmed of surrounding
// space, single-line, and bounded. It never returns raw multiline output.
func quoteBounded(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return BoundedDetail(s)
}
