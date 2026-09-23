package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// JSON-RPC 2.0 framing and parsing primitives for the future stdio
// transport. This layer classifies single messages only: batch arrays are
// rejected, server-initiated requests are classified (never handled), and
// every failure is deterministic and bounded. It never panics on hostile
// input and never performs I/O.
//
// Wire rules enforced here:
//   - exactly "jsonrpc": "2.0"
//   - no batch arrays (top-level [...] is rejected)
//   - requests carry a valid id plus a method; notifications carry a method
//     with absent or null id; responses carry a valid id plus exactly one
//     of result/error and no method
//   - identifiers are strings (non-empty) or integers; booleans, objects,
//     arrays, and null response ids are rejected
//   - MaxMessageBytes is enforced before parsing so hostile frames cannot
//     force unbounded allocation

// MessageKind classifies one parsed JSON-RPC message.
type MessageKind int

const (
	// KindRequest is a peer-initiated call expecting a reply. In v1 the
	// client never answers server-initiated requests: the later transport
	// layer maps them onto ErrProtocol.
	KindRequest MessageKind = iota + 1
	// KindNotification is a peer-initiated signal with no reply. v1 sends
	// notifications/initialized only; others are ignored.
	KindNotification
	// KindResponse is a result or error reply to a client request.
	// Exactly one of Result and Error is set.
	KindResponse
)

// String reports the message kind for diagnostics. It never includes
// message content.
func (k MessageKind) String() string {
	switch k {
	case KindRequest:
		return "request"
	case KindNotification:
		return "notification"
	case KindResponse:
		return "response"
	default:
		return "unknown"
	}
}

// RPCError is one JSON-RPC error object. Message comes from the peer and
// must pass through BoundedDetail before reaching users.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error renders a server error-response for Go callers. The peer's
// message is bounded so error values can never carry unbounded
// server-controlled data. Use errors.As to recover the code.
func (e *RPCError) Error() string {
	if e == nil {
		return "mcp: server error"
	}
	return fmt.Sprintf("mcp: server error %d: %s", e.Code, BoundedDetail(e.Message))
}

// Message is one classified JSON-RPC message. ID is the normalized
// identifier (absent for notifications); Method is set for requests and
// notifications; exactly one of Result and Error is set for responses.
type Message struct {
	Kind   MessageKind
	ID     string
	HasID  bool
	Method string
	Params json.RawMessage
	Result json.RawMessage
	Error  *RPCError
}

// rawEnvelope mirrors the wire shape with IDs and payloads deferred so
// each field can be validated in order with bounded errors.
type rawEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  json.RawMessage `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// ParseMessage classifies one complete JSON-RPC message frame. data is one
// newline-delimited frame; framing (splitting lines) belongs to the later
// transport layer. Unknown fields are ignored. Failures are deterministic:
// malformed JSON, version mismatch, batch arrays, missing or invalid ids,
// and malformed shapes all return typed errors without panicking.
func ParseMessage(data []byte) (Message, error) {
	if len(data) > MaxMessageBytes {
		return Message{}, fmt.Errorf("mcp: message of %d bytes exceeds %d byte limit: %w", len(data), MaxMessageBytes, ErrTooLarge)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return Message{}, fmt.Errorf("mcp: empty frame: %w", ErrInvalidMessage)
	}
	if trimmed[0] == '[' {
		return Message{}, fmt.Errorf("mcp: batch arrays are not supported: %w", ErrProtocol)
	}

	var env rawEnvelope
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&env); err != nil {
		return Message{}, fmt.Errorf("mcp: malformed JSON (%s): %w", quoteBounded(err.Error()), ErrInvalidMessage)
	}
	if env.JSONRPC != "2.0" {
		return Message{}, fmt.Errorf("mcp: jsonrpc version %q must be \"2.0\": %w", quoteBounded(env.JSONRPC), ErrProtocol)
	}

	hasMethod := len(bytes.TrimSpace(env.Method)) > 0
	hasResult := len(bytes.TrimSpace(env.Result)) > 0
	hasError := len(bytes.TrimSpace(env.Error)) > 0
	hasID := len(bytes.TrimSpace(env.ID)) > 0 && strings.TrimSpace(string(env.ID)) != "null"

	if hasMethod {
		method, err := parseMethod(env.Method)
		if err != nil {
			return Message{}, err
		}
		if hasResult || hasError {
			return Message{}, invalidMessagef("method %q must not carry result or error", quoteBounded(method))
		}
		if !hasID {
			return Message{Kind: KindNotification, Method: method, Params: env.Params}, nil
		}
		id, err := NormalizeID(env.ID)
		if err != nil {
			return Message{}, err
		}
		return Message{Kind: KindRequest, ID: id, HasID: true, Method: method, Params: env.Params}, nil
	}

	if !hasID {
		return Message{}, invalidMessagef("response without id")
	}
	id, err := NormalizeID(env.ID)
	if err != nil {
		return Message{}, err
	}
	switch {
	case hasResult && hasError:
		return Message{}, invalidMessagef("response carries both result and error")
	case hasResult:
		return Message{Kind: KindResponse, ID: id, HasID: true, Result: env.Result}, nil
	case hasError:
		rpcErr, err := parseRPCError(env.Error)
		if err != nil {
			return Message{}, err
		}
		return Message{Kind: KindResponse, ID: id, HasID: true, Error: rpcErr}, nil
	default:
		return Message{}, invalidMessagef("response carries neither result nor error")
	}
}

// parseMethod validates a JSON-RPC method value: a non-empty string.
func parseMethod(raw json.RawMessage) (string, error) {
	var method string
	if err := json.Unmarshal(raw, &method); err != nil {
		return "", fmt.Errorf("mcp: method must be a string: %w", ErrInvalidMessage)
	}
	if strings.TrimSpace(method) == "" {
		return "", fmt.Errorf("mcp: method must not be empty: %w", ErrInvalidMessage)
	}
	return method, nil
}

// parseRPCError validates a JSON-RPC error object: integer code plus a
// string message. Data rides along untouched for later layers to ignore.
func parseRPCError(raw json.RawMessage) (*RPCError, error) {
	var obj struct {
		Code    *int            `json:"code"`
		Message *string         `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("mcp: malformed error object: %w", ErrInvalidMessage)
	}
	if obj.Code == nil {
		return nil, fmt.Errorf("mcp: error object without code: %w", ErrInvalidMessage)
	}
	if obj.Message == nil {
		return nil, fmt.Errorf("mcp: error object without message: %w", ErrInvalidMessage)
	}
	return &RPCError{Code: *obj.Code, Message: *obj.Message, Data: obj.Data}, nil
}

// NormalizeID validates a raw JSON-RPC identifier and returns its pending-
// map key. Strings must be non-empty within MaxIDStringLen; numbers must be
// integer-valued JSON numbers. Booleans, objects, arrays, and null are
// rejected so a hostile peer cannot poison request/response matching.
func NormalizeID(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "", fmt.Errorf("mcp: id must not be null or missing: %w", ErrInvalidMessage)
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		if s == "" {
			return "", fmt.Errorf("mcp: string id must not be empty: %w", ErrInvalidMessage)
		}
		if len([]rune(s)) > MaxIDStringLen {
			return "", fmt.Errorf("mcp: string id exceeds %d characters: %w", MaxIDStringLen, ErrTooLarge)
		}
		return s, nil
	}
	var num json.Number
	if err := json.Unmarshal(trimmed, &num); err == nil {
		key := num.String()
		if strings.ContainsAny(key, ".eE") {
			if !isIntegralNumber(key) {
				return "", fmt.Errorf("mcp: numeric id %q must be an integer: %w", quoteBounded(key), ErrInvalidMessage)
			}
			key = canonicalIntegral(key)
		}
		if key == "" || len(key) > MaxIDStringLen {
			return "", fmt.Errorf("mcp: numeric id out of range: %w", ErrInvalidMessage)
		}
		return key, nil
	}
	return "", fmt.Errorf("mcp: id must be a string or integer: %w", ErrInvalidMessage)
}

// isIntegralNumber reports whether a JSON number literal has an integral
// value (e.g. "1", "1.0" and "1e3" are integral; "1.5" is not).
func isIntegralNumber(lit string) bool {
	s := strings.TrimPrefix(strings.TrimPrefix(lit, "-"), "+")
	mantissa := s
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa = s[:i]
		var n int
		if _, err := fmt.Sscanf(s[i+1:], "%d", &n); err != nil {
			return false
		}
		exp = n
	}
	intPart := mantissa
	fracPart := ""
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		intPart, fracPart = mantissa[:dot], mantissa[dot+1:]
	}
	if intPart == "" && fracPart == "" {
		return false
	}
	for _, part := range []string{intPart, fracPart} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	// The value is integral exactly when every digit right of the decimal
	// point (after applying the exponent shift) is zero.
	digits := intPart + fracPart
	point := len(intPart) + exp
	var frac string
	switch {
	case point <= 0:
		frac = digits
	case point >= len(digits):
		frac = ""
	default:
		frac = digits[point:]
	}
	for _, r := range frac {
		if r != '0' {
			return false
		}
	}
	return true
}

// canonicalIntegral normalizes an integral JSON number literal to a plain
// digit string so equivalent ids ("1", "1.0", "1e3") share one map key.
// Callers check isIntegralNumber first; non-integral input yields its
// trimmed literal unchanged (and is rejected upstream).
func canonicalIntegral(lit string) string {
	neg := strings.HasPrefix(lit, "-")
	s := strings.TrimPrefix(strings.TrimPrefix(lit, "-"), "+")
	mantissa := s
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa = s[:i]
		fmt.Sscanf(s[i+1:], "%d", &exp)
	}
	intPart := mantissa
	fracPart := ""
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		intPart, fracPart = mantissa[:dot], mantissa[dot+1:]
	}
	digits := intPart + fracPart
	point := len(intPart) + exp // position of the decimal point in digits
	var out string
	switch {
	case point <= 0:
		out = "0"
	case point >= len(digits):
		out = digits + strings.Repeat("0", point-len(digits))
	default:
		out = digits[:point]
	}
	out = strings.TrimLeft(out, "0")
	if out == "" {
		return "0"
	}
	if neg {
		return "-" + out
	}
	return out
}

// MarshalRequest encodes one client request frame with a numeric id. Args
// beyond method validation are the caller's responsibility; params that
// fail to marshal return an error and encode nothing.
func MarshalRequest(id int64, method string, params any) ([]byte, error) {
	if strings.TrimSpace(method) == "" {
		return nil, fmt.Errorf("mcp: request method must not be empty: %w", ErrInvalidMessage)
	}
	var raw json.RawMessage
	if params != nil {
		enc, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("mcp: request params: %w", err)
		}
		raw = enc
	}
	frame := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if raw != nil {
		frame["params"] = json.RawMessage(raw)
	}
	out, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("mcp: encode request: %w", err)
	}
	if len(out) > MaxMessageBytes {
		return nil, fmt.Errorf("mcp: request of %d bytes exceeds %d byte limit: %w", len(out), MaxMessageBytes, ErrTooLarge)
	}
	return out, nil
}

// MarshalNotification encodes one client notification frame (no id).
func MarshalNotification(method string, params any) ([]byte, error) {
	if strings.TrimSpace(method) == "" {
		return nil, fmt.Errorf("mcp: notification method must not be empty: %w", ErrInvalidMessage)
	}
	var raw json.RawMessage
	if params != nil {
		enc, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("mcp: notification params: %w", err)
		}
		raw = enc
	}
	frame := map[string]any{"jsonrpc": "2.0", "method": method}
	if raw != nil {
		frame["params"] = json.RawMessage(raw)
	}
	out, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("mcp: encode notification: %w", err)
	}
	if len(out) > MaxMessageBytes {
		return nil, fmt.Errorf("mcp: notification of %d bytes exceeds %d byte limit: %w", len(out), MaxMessageBytes, ErrTooLarge)
	}
	return out, nil
}
