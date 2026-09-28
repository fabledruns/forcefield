package mcp

import (
	"encoding/json"
	"fmt"
)

// Centralized MCP protocol-version negotiation and the minimal method
// primitives for the v1 scope (initialize, tools/list, tools/call, plus
// the initialized notification). No transport or execution lives here:
// these types and validators are pure so later phases can use them without
// importing process or runtime concerns.
//
// Version policy: the client announces exactly ClientVersion. A server's
// reported version must be an exact member of SupportedVersions; anything
// else (empty, malformed, or future) fails closed. There is no range
// negotiation and no best-effort guessing.

// Method names for the v1 scope. No other method is ever sent; server-side
// methods (sampling, roots, elicitation, logging, progress) are out of
// scope and must be ignored or rejected by later layers, never handled.
const (
	// MethodInitialize starts the handshake.
	MethodInitialize = "initialize"
	// MethodInitialized is the client-to-server notification completing
	// the handshake. It expects no reply.
	MethodInitialized = "notifications/initialized"
	// MethodListTools discovers tools with cursor pagination.
	MethodListTools = "tools/list"
	// MethodCallTool invokes one tool by its server-local name.
	MethodCallTool = "tools/call"
)

// ClientVersion is the single protocol version the client announces in
// initialize. SupportedVersions is the explicit allowlist of server
// versions accepted during negotiation. Both live here and nowhere else
// so the supported set changes in exactly one place.
const ClientVersion = "2025-11-25"

// SupportedVersions lists every server protocolVersion accepted in v1,
// oldest first. Unknown and future versions fail closed via Negotiate.
var SupportedVersions = []string{
	"2024-11-05",
	"2025-03-26",
	"2025-06-18",
	"2025-11-25",
}

// IsSupported reports whether version is an exact member of the
// allowlist. Comparison is exact: no trimming, no prefix matching, no
// downgrade inference.
func IsSupported(version string) bool {
	for _, v := range SupportedVersions {
		if version == v {
			return true
		}
	}
	return false
}

// Negotiate validates a server-reported protocol version. It returns the
// agreed version (the server's, when allowlisted) or a wrapped
// ErrUnsupportedVersion carrying only a bounded excerpt. Empty,
// malformed, and future versions all fail closed.
func Negotiate(serverVersion string) (string, error) {
	if !IsSupported(serverVersion) {
		return "", fmt.Errorf("mcp: protocol version %q is not supported (client speaks %q): %w",
			quoteBounded(serverVersion), ClientVersion, ErrUnsupportedVersion)
	}
	return serverVersion, nil
}

// Implementation identifies one MCP peer (client or server).
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ClientCapabilities declares what the Forcefield client offers. In v1 the
// client offers nothing beyond receiving tools: sampling, roots, and
// elicitation are out of scope and must stay absent so servers cannot
// treat the client as capable of them.
type ClientCapabilities struct{}

// InitializeParams is the initialize request payload the client sends.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      Implementation     `json:"clientInfo"`
}

// InitializeResult is the initialize response payload a server returns.
// Capabilities stays raw: only the presence of the "tools" member is
// checked, and every other capability is ignored.
type InitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ServerInfo      Implementation  `json:"serverInfo"`
}

// ValidateInitializeResult checks an initialize result without I/O: the
// version must negotiate and the capabilities must declare tools support.
// It returns the agreed version. Extra capabilities are ignored; a
// missing "tools" member fails the server.
func ValidateInitializeResult(res InitializeResult) (string, error) {
	agreed, err := Negotiate(res.ProtocolVersion)
	if err != nil {
		return "", err
	}
	if !HasToolsCapability(res.Capabilities) {
		return "", fmt.Errorf("mcp: server does not declare the tools capability: %w", ErrProtocol)
	}
	return agreed, nil
}

// HasToolsCapability reports whether a raw capabilities object declares
// the "tools" member. Non-object payloads (including absent capabilities)
// report false. Unknown members are ignored.
func HasToolsCapability(caps json.RawMessage) bool {
	if len(caps) == 0 {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(caps, &obj); err != nil {
		return false
	}
	_, ok := obj["tools"]
	return ok
}

// ListToolsParams is the tools/list request payload. Cursor is absent on
// the first page and echoed from the previous result's NextCursor after.
type ListToolsParams struct {
	Cursor *string `json:"cursor,omitempty"`
}

// ListToolsResult is one tools/list response page. Tool entries stay raw:
// each entry is validated independently by the sanitization layer so one
// malformed entry skips one tool instead of failing the page.
type ListToolsResult struct {
	Tools      []json.RawMessage `json:"tools"`
	NextCursor *string           `json:"nextCursor,omitempty"`
}

// ValidateListPage checks one tools/list page envelope without interpreting
// entries: Tools must be present (nil reads as absent and fails), and an
// empty-string cursor is normalized away by callers. Entry count against
// MaxToolsPerServer is aggregated by the later discovery layer, which owns
// cross-page totals; this check only rejects a negative-shaped envelope.
func ValidateListPage(res ListToolsResult) error {
	if res.Tools == nil {
		return fmt.Errorf("mcp: tools/list response without tools array: %w", ErrInvalidMessage)
	}
	return nil
}

// CallToolParams is the tools/call request payload. Name is the
// server-local tool name; Arguments is an object (nil encodes as absent
// and is sent as an empty object by later layers).
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ContentPart is one entry of a tools/call result content array. Only the
// "text" shape is carried into model context in v1; every other type is
// replaced with a placeholder by later layers. Unknown fields are ignored.
type ContentPart struct {
	Type string          `json:"type"`
	Text *string         `json:"text,omitempty"`
	Raw  json.RawMessage `json:"-"`
}

// CallToolResult is the tools/call response payload. Structured content
// rides along for later layers to marshal to text; IsError marks a
// server-reported soft failure (the call worked, the tool declined).
type CallToolResult struct {
	Content           []ContentPart `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

// ValidateCallParams checks a tools/call request shape before it is sent:
// the name must be non-empty within the remote-name bound. Argument shape
// is enforced against the sanitized tool schema by the scheduler and the
// adapter, not here.
func ValidateCallParams(p CallToolParams) error {
	if p.Name == "" {
		return fmt.Errorf("mcp: tools/call without tool name: %w", ErrInvalidMessage)
	}
	if len([]rune(p.Name)) > MaxRemoteToolNameLen {
		return fmt.Errorf("mcp: tool name exceeds %d characters: %w", MaxRemoteToolNameLen, ErrTooLarge)
	}
	return nil
}
