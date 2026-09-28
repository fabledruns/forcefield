package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"forcefield/internal/tools"
)

// MCP tool adapter: exposes one discovered MCP tool through Forcefield's
// existing tools.Tool abstraction without changing the registry, the
// scheduler, or any permission logic.
//
// Identity split (never confused):
//   - Qualified (mcp__server__remote) is the local Forcefield identity:
//     registry keys, definitions, permission decisions, and results.
//   - Name (the original server-advertised remote name) is what tools/call
//     sends. The qualified name never goes on the wire.
//
// The adapter is stateless apart from its validated snapshot copy and
// configurable limits: concurrent Execute calls simply become concurrent
// client requests, which the transport correlates. It owns no lifecycle:
// creation starts nothing and Execute never closes the client, the
// transport, or any stream. The future Host layer owns all of that.
//
// Deliberately NOT implemented: BoundaryChecker. Opaque MCP arguments
// cannot be interpreted as filesystem operations, so the adapter makes no
// sandbox claims; workspace confinement of whatever the server itself
// does is outside Forcefield's boundary by design (see Revision 2).

// Tool adapts one discovered MCP tool to tools.Tool.
type Tool struct {
	client      *Client
	server      string
	remote      string
	qualified   string
	description string
	schema      map[string]any
	limits      tools.Limits
}

// Compile-time conformance: the adapter satisfies the core interface plus
// the metadata/limits options the scheduler and runtime consult. It must
// never satisfy BoundaryChecker or StreamingTool; see the package note.
var (
	_ tools.Tool             = (*Tool)(nil)
	_ tools.MetadataProvider = (*Tool)(nil)
	_ tools.LimitsSetter     = (*Tool)(nil)
	_ tools.LimitsProvider   = (*Tool)(nil)
)

// NewTool wraps def for client as a Forcefield tool. def must come from
// the client's own discovery snapshot: the qualified name must match the
// server key plus the sanitized remote name, and the server key must match
// the client, otherwise construction fails closed instead of repairing
// the definition. The schema is deep-copied so neither the client's
// snapshot nor later callers can mutate the adapter through it.
func NewTool(client *Client, def DiscoveredTool) (*Tool, error) {
	if client == nil {
		return nil, fmt.Errorf("mcp: tool adapter requires a non-nil client: %w", ErrInvalidConfig)
	}
	if def.Server == "" || def.Name == "" {
		return nil, fmt.Errorf("mcp: tool adapter requires server and remote names: %w", ErrInvalidTool)
	}
	if !IsQualifiedToolName(def.Qualified) {
		return nil, fmt.Errorf("mcp: tool adapter requires a qualified name, got %q: %w", BoundedDetail(def.Qualified), ErrInvalidTool)
	}
	if def.Server != client.ServerKey() {
		return nil, fmt.Errorf("mcp: tool %q belongs to server %q, not client server %q: %w",
			BoundedDetail(def.Qualified), BoundedDetail(def.Server), BoundedDetail(client.ServerKey()), ErrInvalidTool)
	}
	want, err := QualifiedName(def.Server, def.Name)
	if err != nil {
		return nil, fmt.Errorf("mcp: tool %q has an unsanitizable remote name: %w", BoundedDetail(def.Qualified), err)
	}
	if want != def.Qualified {
		return nil, fmt.Errorf("mcp: tool qualified name %q does not match server %q plus remote name: %w",
			BoundedDetail(def.Qualified), BoundedDetail(def.Server), ErrInvalidTool)
	}
	// Descriptions from discovery are already bounded; truncating again is
	// a no-op for them and a fail-safe for any other caller, so remote
	// text can never make the adapter unbounded.
	description, _ := TruncateDescription(def.Description)
	return &Tool{
		client:      client,
		server:      def.Server,
		remote:      def.Name,
		qualified:   def.Qualified,
		description: description,
		schema:      copySchema(def.InputSchema),
	}, nil
}

// Name returns the qualified Forcefield identity (mcp__server__remote).
func (t *Tool) Name() string { return t.qualified }

// Description returns the bounded description produced at discovery. It
// is already truncated; no second pass runs here.
func (t *Tool) Description() string { return t.description }

// InputSchema returns a deep copy of the validated discovery schema.
// Copies keep the adapter snapshot read-only no matter what callers do
// with the returned map.
func (t *Tool) InputSchema() map[string]any { return copySchema(t.schema) }

// ServerKey returns the MCP server key this tool was discovered from.
func (t *Tool) ServerKey() string { return t.server }

// RemoteName returns the original server-advertised tool name sent in
// tools/call. It is the only name the wire ever sees.
func (t *Tool) RemoteName() string { return t.remote }

// Metadata advertises execution characteristics to the scheduler.
// MCP calls are cancellable and parallel-safe at this layer, never
// streamed, and never retried: remote invocations are not idempotent in
// general. No special permissions are claimed here; authorization stays
// with the permission system in a later phase.
func (t *Tool) Metadata() tools.Metadata {
	return tools.Metadata{
		Timeout:              t.resolveLimits().Timeout,
		SupportsStreaming:    false,
		SupportsCancellation: true,
		SupportsParallel:     true,
		Retryable:            false,
	}
}

// SetLimits overrides the adapter bounds. Only positive fields take
// effect; the rest resolve to the tool defaults. It follows the native
// convention: applied right after construction, before registration, so
// limits survive Filtered (which reuses the same instances).
func (t *Tool) SetLimits(l tools.Limits) { t.limits = l }

// ToolLimits reports the resolved bounds so the scheduler can apply the
// configured timeout end to end.
func (t *Tool) ToolLimits() tools.Limits { return t.resolveLimits() }

func (t *Tool) resolveLimits() tools.Limits {
	if t == nil {
		return tools.Limits{Timeout: tools.DefaultToolTimeout}
	}
	// Unknown names get the safe generic default (30s timeout, no byte
	// cap of their own); the transport frame cap and the runtime context
	// guard still bound output downstream.
	return t.limits.WithDefaults(tools.Limits{Timeout: tools.DefaultToolTimeout})
}

// Execute runs the remote tool through the client and converts the MCP
// result into a Forcefield result. The scheduler owns argument validation
// against InputSchema and the authoritative execution timeout; this method
// performs only the protocol-level conversion the wire requires. Hard
// failures (transport, RPC, protocol, closed/unready client) return errors
// with their sentinels intact; a server-reported isError becomes a soft
// Result.IsError so the model can adapt.
func (t *Tool) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	start := time.Now()
	if !t.client.Ready() {
		// Ready() is false for failed, closed, and transport-dead
		// clients alike; State separates shutdown (ErrClosed) from
		// every other unusable case (ErrTransport) without reconnecting.
		if t.client.State() == StateClosed {
			return tools.Result{}, fmt.Errorf("mcp tool %q: client closed: %w", t.qualified, ErrClosed)
		}
		return tools.Result{}, fmt.Errorf("mcp tool %q: client not ready: %w", t.qualified, ErrTransport)
	}
	if args == nil {
		args = map[string]any{}
	}
	res, err := t.client.CallTool(ctx, t.remote, args)
	if err != nil {
		return tools.Result{}, err
	}
	content := renderCallResult(res)
	maxBytes := t.resolveLimits().MaxBytes
	content, trunc := tools.TruncateString(content, maxBytes)
	out := tools.Result{
		Content:    content,
		IsError:    res.IsError,
		DurationMs: time.Since(start).Milliseconds(),
		Tool:       t.qualified,
	}
	if trunc.Truncated {
		out.Metadata = trunc.Fields()
		out.Content += tools.TruncationNote(trunc.KeptBytes, trunc.OriginalBytes)
	}
	return out, nil
}

// renderCallResult flattens an MCP result into model text. Text parts
// carry over verbatim; every other part type becomes a bounded
// placeholder (binary and resource payloads never enter model context as
// raw bytes). With no text parts, structured content marshals to JSON as
// the fallback; with neither, the result is honestly empty.
func renderCallResult(res CallToolResult) string {
	var parts []string
	for _, p := range res.Content {
		if p.Type == "text" && p.Text != nil {
			parts = append(parts, *p.Text)
			continue
		}
		typ := BoundedDetail(p.Type)
		if strings.TrimSpace(typ) == "" {
			typ = "unknown"
		}
		parts = append(parts, "[non-text content omitted: "+typ+"]")
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if enc, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(enc))
		} else {
			parts = append(parts, "[structured content omitted: unmarshalable]")
		}
	}
	return strings.Join(parts, "\n")
}

// copySchema deep-copies JSON-like schema values so snapshots stay
// read-only. Scalars are immutable and shared; anything outside the JSON
// value space cannot occur in decoded schemas and passes through
// untouched rather than failing.
func copySchema(s map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	out, _ := copyJSONValue(s).(map[string]any)
	return out
}

// copyJSONValue copies maps, slices, and scalars recursively.
func copyJSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = copyJSONValue(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = copyJSONValue(e)
		}
		return out
	default:
		return v
	}
}
