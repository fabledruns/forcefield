package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// MCP client protocol layer over the Phase 2 transport: initialization
// with version negotiation and capability validation, followed by bounded
// tools/list discovery with per-tool validation. The client owns no
// process, no streams, and no registry state: it drives exactly three
// outbound shapes (initialize, notifications/initialized, tools/list)
// through Transport.Request/Notify and keeps the validated snapshot.
//
// Trust posture inherits the package invariant: every inbound value is
// untrusted. Versions negotiate against the centralized allowlist,
// capabilities gate on the tools member, and each tool entry is parsed,
// bounded, sanitized, and namespaced independently so one malformed entry
// skips one tool instead of failing discovery.
//
// The client is sequential by design: it adds no request multiplexer, no
// pending map, and no goroutines. Transport owns correlation; this layer
// owns protocol order and validation.

// ClientState is the client's lifecycle position. Transitions run one
// way: New → Initializing → Initialized → Discovering → Ready, with
// Failed and Closed terminal from any state. States exist to prevent
// invalid transitions (use before init, double init, discovery before
// init); anything subtler would be over-engineering for a startup-only
// handshake.
type ClientState int

const (
	// StateNew is a constructed client that has done no I/O.
	StateNew ClientState = iota + 1
	// StateInitializing is an in-flight initialize handshake.
	StateInitializing
	// StateInitialized completed the handshake (version + capabilities +
	// initialized notification) but has no tool snapshot yet.
	StateInitialized
	// StateDiscovering is an in-flight tools/list walk.
	StateDiscovering
	// StateReady holds a validated tool snapshot. Effective readiness
	// also requires a healthy transport; see Ready.
	StateReady
	// StateFailed is a terminal handshake or discovery failure. The
	// client stays unusable; Close still works.
	StateFailed
	// StateClosed is a terminal explicit shutdown.
	StateClosed
)

// String reports the state name for diagnostics. It carries no payloads.
func (s ClientState) String() string {
	switch s {
	case StateNew:
		return "new"
	case StateInitializing:
		return "initializing"
	case StateInitialized:
		return "initialized"
	case StateDiscovering:
		return "discovering"
	case StateReady:
		return "ready"
	case StateFailed:
		return "failed"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// MaxDiscoveryWarnings bounds retained per-tool skip diagnostics. Skips
// beyond the cap still increment the skipped count, so a hostile server
// cannot flood diagnostics while the total stays observable.
const MaxDiscoveryWarnings = 32

// MaxCursorBytes bounds one tools/list continuation cursor. Cursors are
// opaque server tokens echoed back verbatim; the cap keeps a single
// retained token sane without constraining legitimate pagination.
const MaxCursorBytes = MaxSchemaStringBytes

// DiscoveredTool is one validated tool definition from tools/list. Name
// is the original server-advertised name and is what a later tools/call
// sends; Qualified is the Forcefield identity (mcp__server__sanitized)
// used for registry and permission decisions. Description is truncated
// plain text; InputSchema is nil when the server sent none.
type DiscoveredTool struct {
	Server      string
	Name        string
	Qualified   string
	Description string
	InputSchema map[string]any
}

// DefaultClientInfo returns the deterministic client identity sent in
// initialize: the module/binary name with the same "dev" default the CLI
// uses when no build-time version is stamped. The future Host layer may
// pass explicit metadata instead; nothing here reads configuration.
func DefaultClientInfo() Implementation {
	return Implementation{Name: "forcefield", Version: "dev"}
}

// Client is one MCP server session's protocol state. It is safe for
// concurrent accessors, but Initialize and Discover are startup-only and
// must not run concurrently with each other; second calls fail instead
// of serializing. The underlying transport remains the concurrency owner
// for request correlation.
type Client struct {
	// mu guards lifecycle state and the snapshot. I/O runs outside mu:
	// transitions set the in-flight state, release mu, do requests, then
	// commit under mu exactly once.
	mu        sync.Mutex
	transport *Transport
	server    string
	info      Implementation
	state     ClientState
	agreed    string
	peer      string
	tools     []DiscoveredTool
	warnings  []string
	skipped   int
}

// NewClient binds protocol state to an existing transport for one server.
// It performs no I/O and starts no goroutines. serverKey must be valid;
// info.Name must be non-empty (size is bounded downstream by the frame
// cap). The transport's streams stay owned by the caller.
func NewClient(t *Transport, serverKey string, info Implementation) (*Client, error) {
	if t == nil {
		return nil, fmt.Errorf("mcp: client requires a non-nil transport")
	}
	if err := ValidateServerKey(serverKey); err != nil {
		return nil, err
	}
	if strings.TrimSpace(info.Name) == "" {
		return nil, fmt.Errorf("mcp: client name must not be empty: %w", ErrInvalidMessage)
	}
	return &Client{transport: t, server: serverKey, info: info, state: StateNew}, nil
}

// Initialize runs the handshake: initialize with the pinned client
// version, negotiation and capability validation of the result, then
// notifications/initialized. Initialized is sent only after the result
// validates; any failure (transport, RPC, version, capability, malformed
// result, notify) leaves the client Failed with no retry. Exactly one
// successful call is allowed.
func (c *Client) Initialize(ctx context.Context) error {
	c.mu.Lock()
	if c.state != StateNew {
		err := c.stateError("initialize")
		c.mu.Unlock()
		return err
	}
	c.state = StateInitializing
	tr, info := c.transport, c.info
	c.mu.Unlock()

	agreed, peer, err := runInitialize(ctx, tr, info)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.failLocked()
		return err
	}
	c.agreed, c.peer = agreed, peer
	c.state = StateInitialized
	return nil
}

// runInitialize performs the handshake I/O without touching client state
// so Initialize stays a thin guarded wrapper.
func runInitialize(ctx context.Context, tr *Transport, info Implementation) (agreed, peer string, err error) {
	msg, err := tr.Request(ctx, MethodInitialize, InitializeParams{
		ProtocolVersion: ClientVersion,
		Capabilities:    ClientCapabilities{},
		ClientInfo:      info,
	})
	if err != nil {
		return "", "", err
	}
	var res InitializeResult
	trimmed := trimSpace(msg.Result)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "", "", fmt.Errorf("mcp: initialize response without result: %w", ErrInvalidMessage)
	}
	if err := json.Unmarshal(trimmed, &res); err != nil {
		return "", "", fmt.Errorf("mcp: malformed initialize result (%s): %w", quoteBounded(err.Error()), ErrInvalidMessage)
	}
	agreed, err = ValidateInitializeResult(res)
	if err != nil {
		return "", "", err
	}
	// The handshake completed: only now is initialized sent. A failed
	// validation above returns before this line, so failure never emits it.
	if err := tr.Notify(MethodInitialized, nil); err != nil {
		return "", "", err
	}
	return agreed, BoundedDetail(res.ServerInfo.Name), nil
}

// Discover walks tools/list with bounded pagination and validates every
// entry independently. Valid tools accumulate up to MaxToolsPerServer;
// malformed entries skip with bounded warnings; envelope, pagination, or
// transport failures fail discovery. Exactly one successful call is
// allowed; the snapshot freezes at Ready with no live updates after.
func (c *Client) Discover(ctx context.Context) error {
	c.mu.Lock()
	if c.state != StateInitialized {
		err := c.stateError("discover")
		c.mu.Unlock()
		return err
	}
	c.state = StateDiscovering
	tr, server := c.transport, c.server
	c.mu.Unlock()

	tools, warnings, skipped, err := runDiscovery(ctx, tr, server)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.failLocked()
		return err
	}
	c.tools, c.warnings, c.skipped = tools, warnings, skipped
	c.state = StateReady
	return nil
}

// runDiscovery performs paginated tools/list without touching client
// state. Pages stop at the first absent or empty cursor; cursors repeat,
// oversized cursors, page-count exhaustion, and envelope failures are
// fatal, while single malformed entries skip with warnings.
func runDiscovery(ctx context.Context, tr *Transport, server string) ([]DiscoveredTool, []string, int, error) {
	var out []DiscoveredTool
	var warnings []string
	skipped := 0
	warn := func(reason string) {
		skipped++
		if len(warnings) < MaxDiscoveryWarnings {
			warnings = append(warnings, BoundedWarning(reason))
		}
	}
	index := make(map[string]int)
	poisoned := make(map[string]bool)
	dropped := make([]bool, 0)
	retained := 0
	seenCursors := make(map[string]bool)
	var cursor *string
	for page := 0; page < MaxListPages; page++ {
		msg, err := tr.Request(ctx, MethodListTools, ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, nil, 0, err
		}
		var res ListToolsResult
		if err := json.Unmarshal(trimSpace(msg.Result), &res); err != nil {
			return nil, nil, 0, fmt.Errorf("mcp: malformed tools/list result (%s): %w", quoteBounded(err.Error()), ErrInvalidMessage)
		}
		if err := ValidateListPage(res); err != nil {
			return nil, nil, 0, err
		}
		for _, raw := range res.Tools {
			if retained >= MaxToolsPerServer {
				return nil, nil, 0, fmt.Errorf("mcp: server advertised more than %d tools: %w", MaxToolsPerServer, ErrTooLarge)
			}
			tool, err := ParseToolEntry(raw)
			if err != nil {
				warn(fmt.Sprintf("skipping malformed tool entry: %v", err))
				continue
			}
			if err := ValidateRemoteTool(tool); err != nil {
				warn(fmt.Sprintf("skipping tool %q: %v", tool.Name, err))
				continue
			}
			qualified, err := QualifiedName(server, tool.Name)
			if err != nil {
				warn(fmt.Sprintf("skipping tool %q: %v", tool.Name, err))
				continue
			}
			if poisoned[qualified] {
				warn(fmt.Sprintf("skipping tool %q: duplicate qualified name %q", tool.Name, qualified))
				continue
			}
			if i, dup := index[qualified]; dup {
				// Fail closed on intra-server collisions: every instance
				// with this qualified name is dropped, including the one
				// kept earlier, so nothing silently overwrites anything.
				// The result is order-independent.
				warn(fmt.Sprintf("skipping tool %q: duplicate qualified name %q (also advertised as %q)", tool.Name, qualified, out[i].Name))
				if !dropped[i] {
					dropped[i] = true
					retained--
				}
				poisoned[qualified] = true
				delete(index, qualified)
				continue
			}
			desc, _ := TruncateDescription(tool.Description)
			out = append(out, DiscoveredTool{
				Server:      server,
				Name:        tool.Name,
				Qualified:   qualified,
				Description: desc,
				InputSchema: tool.InputSchema,
			})
			dropped = append(dropped, false)
			index[qualified] = len(out) - 1
			retained++
		}
		next := res.NextCursor
		if next == nil || strings.TrimSpace(*next) == "" {
			return finalizeDiscovery(out, dropped), warnings, skipped, nil
		}
		if len(*next) > MaxCursorBytes {
			return nil, nil, 0, fmt.Errorf("mcp: tools/list cursor of %d bytes exceeds %d byte limit: %w", len(*next), MaxCursorBytes, ErrTooLarge)
		}
		if seenCursors[*next] {
			return nil, nil, 0, fmt.Errorf("mcp: tools/list repeated cursor %q: %w", quoteBounded(*next), ErrProtocol)
		}
		seenCursors[*next] = true
		cursor = next
	}
	return nil, nil, 0, fmt.Errorf("mcp: tools/list did not terminate within %d pages: %w", MaxListPages, ErrTooLarge)
}

// finalizeDiscovery drops poisoned duplicates, preserving first-seen
// order for the survivors.
func finalizeDiscovery(out []DiscoveredTool, dropped []bool) []DiscoveredTool {
	kept := out[:0]
	for i, tool := range out {
		if !dropped[i] {
			kept = append(kept, tool)
		}
	}
	return kept
}

// stateError reports lifecycle misuse. Closed operations surface ErrClosed
// so callers can distinguish shutdown from flow violations; every other
// misuse is a client-flow protocol error.
func (c *Client) stateError(op string) error {
	if c.state == StateClosed {
		return fmt.Errorf("mcp: client %s on closed client: %w", op, ErrClosed)
	}
	return fmt.Errorf("mcp: client %s in state %s: %w", op, c.state, ErrProtocol)
}

// failLocked moves a non-closed client to Failed. A concurrent Close wins
// over failure bookkeeping: shutdown stays the reported state.
func (c *Client) failLocked() {
	if c.state != StateClosed {
		c.state = StateFailed
	}
}

// State returns the raw lifecycle state. Effective readiness is Ready():
// a Ready client over a dead transport reports State()==Ready with
// Ready()==false, because stream truth lives in the transport and the
// client runs no monitor goroutine.
func (c *Client) State() ClientState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Ready reports whether the client holds a validated snapshot over a
// healthy transport. Transport failure flips this to false with no
// reconnect, respawn, or re-initialization.
func (c *Client) Ready() bool {
	c.mu.Lock()
	ready := c.state == StateReady
	c.mu.Unlock()
	return ready && c.transport.Healthy()
}

// Tools returns a copy of the validated snapshot. It fails fast unless
// the client is effectively ready, so callers can never use tools from a
// failed, closed, or transport-dead client.
func (c *Client) Tools() ([]DiscoveredTool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateReady {
		return nil, c.stateError("tools")
	}
	if !c.transport.Healthy() {
		return nil, fmt.Errorf("mcp: client tools unavailable: transport unhealthy: %w", ErrTransport)
	}
	return append([]DiscoveredTool(nil), c.tools...), nil
}

// Warnings returns the bounded skip diagnostics recorded during
// discovery, oldest first. Same readiness gate as Tools.
func (c *Client) Warnings() ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateReady {
		return nil, c.stateError("warnings")
	}
	if !c.transport.Healthy() {
		return nil, fmt.Errorf("mcp: client warnings unavailable: transport unhealthy: %w", ErrTransport)
	}
	return append([]string(nil), c.warnings...), nil
}

// Skipped counts tool entries skipped during discovery, including those
// past the retained-warning cap. Same readiness gate as Tools.
func (c *Client) Skipped() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateReady {
		return 0, c.stateError("skipped")
	}
	if !c.transport.Healthy() {
		return 0, fmt.Errorf("mcp: client skipped count unavailable: transport unhealthy: %w", ErrTransport)
	}
	return c.skipped, nil
}

// CallTool invokes one remote tool by its original server-advertised
// name with the given argument object. It is the minimal call primitive
// the tool adapter needs: no other MCP behavior is added here. The call
// fails fast unless the client is effectively ready (no reconnect,
// respawn, or re-initialization on transport failure). Server
// error-responses surface as (*RPCError); malformed results are
// protocol failures. Nil args encode as an empty object.
func (c *Client) CallTool(ctx context.Context, remote string, args map[string]any) (CallToolResult, error) {
	c.mu.Lock()
	if c.state != StateReady {
		err := c.stateError("call")
		c.mu.Unlock()
		return CallToolResult{}, err
	}
	tr := c.transport
	healthy := tr.Healthy()
	c.mu.Unlock()
	if !healthy {
		return CallToolResult{}, fmt.Errorf("mcp: client call unavailable: transport unhealthy: %w", ErrTransport)
	}
	if err := ValidateCallParams(CallToolParams{Name: remote}); err != nil {
		return CallToolResult{}, err
	}
	if args == nil {
		args = map[string]any{}
	}
	msg, err := tr.Request(ctx, MethodCallTool, CallToolParams{Name: remote, Arguments: args})
	if err != nil {
		return CallToolResult{}, err
	}
	var res CallToolResult
	trimmed := trimSpace(msg.Result)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return CallToolResult{}, fmt.Errorf("mcp: tools/call response without result: %w", ErrInvalidMessage)
	}
	if err := json.Unmarshal(trimmed, &res); err != nil {
		return CallToolResult{}, fmt.Errorf("mcp: malformed tools/call result (%s): %w", quoteBounded(err.Error()), ErrInvalidMessage)
	}
	return res, nil
}

// ServerKey returns the configured server key. It is immutable.
func (c *Client) ServerKey() string {
	return c.server
}

// AgreedVersion returns the negotiated protocol version, or "" before a
// successful handshake.
func (c *Client) AgreedVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agreed
}

// PeerName returns the bounded server-reported name, or "" before a
// successful handshake.
func (c *Client) PeerName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peer
}

// Close shuts the client down: it becomes unusable, prevents new protocol
// operations, and shuts the underlying transport. It never touches the
// caller-owned streams (the future Host closes those and waits on the
// transport's Done). Close is idempotent.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.state == StateClosed {
		c.mu.Unlock()
		return nil
	}
	c.state = StateClosed
	tr := c.transport
	c.mu.Unlock()
	_ = tr.Close()
	return nil
}

// trimSpace is a byte-level whitespace trim for result payloads.
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isSpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

// isSpace matches JSON insignificant whitespace.
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
