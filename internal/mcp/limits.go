package mcp

// Size and count bounds for untrusted MCP data and MCP configuration.
//
// Every constant names what it protects and where it is enforced. Values
// follow the Revision 2 plan provisionally; changing a value requires
// updating the matching test boundary case and the CUE schema where the
// bound is also expressible. MCP limits and the runtime's model-output
// guard (session.MaxModelToolResultChars) remain conceptually distinct:
// the former bound what a server may send, the latter bounds what the
// model ever sees.
const (
	// MaxServers bounds how many MCP servers may be enabled at once.
	// It protects startup/shutdown work and process fan-out. Enforced in
	// Config.Validate (configuration layer).
	MaxServers = 8

	// MinServerKeyLen and MaxServerKeyLen bound MCP server-key length.
	// They keep qualified tool names predictable and within provider
	// function-name grammars. Enforced in ValidateServerKey.
	MinServerKeyLen = 1
	MaxServerKeyLen = 32

	// MaxCommandBytes bounds a configured server executable path or name.
	// It prevents absurd argv blocks from reaching a future process layer.
	// Enforced in ServerConfig validation (shape only; resolution via
	// LookPath happens at startup, never at config load).
	MaxCommandBytes = 4096

	// MaxArgBytes bounds one configured argv element, and MaxArgsTotalBytes
	// bounds the whole configured argv block. They prevent argv-based
	// memory pressure before anything reaches a future process layer.
	// Enforced in ServerConfig validation.
	MaxArgBytes       = 4096
	MaxArgsTotalBytes = 64 * 1024

	// MaxCwdBytes bounds a configured server working directory string.
	// Existence and directory checks happen at startup against the
	// workspace root; this bound only keeps the string itself sane.
	// Enforced in ServerConfig validation.
	MaxCwdBytes = 4096

	// MaxEnvEntries bounds literal extra environment entries, and
	// MaxEnvValueBytes bounds one literal value. They bound the
	// constructed child-environment block. Enforced in ServerConfig
	// validation. Values are literal: no variable expansion exists in v1.
	MaxEnvEntries    = 64
	MaxEnvValueBytes = 8 * 1024

	// MaxPassthroughEntries bounds host-variable passthrough allowlist
	// entries. Passthrough defaults to empty and never inherits the host
	// environment wholesale. Enforced in ServerConfig validation.
	MaxPassthroughEntries = 64

	// DefaultServerTimeoutSeconds is the per tools/call attempt timeout
	// applied when timeout_seconds is omitted or zero. It matches the
	// native tool default so MCP calls behave like other tools.
	DefaultServerTimeoutSeconds = 30.0

	// MaxServerTimeoutSeconds is the hard ceiling for a configured
	// per-server attempt timeout. It mirrors the scheduler's hard ceiling
	// so no tool (and no misconfigured override) can run unbounded.
	// Enforced in ServerConfig validation.
	MaxServerTimeoutSeconds = 300.0

	// MaxMessageBytes bounds one JSON-RPC protocol message. It is enforced
	// BEFORE JSON parsing so a hostile frame cannot force unbounded
	// allocation. Enforced in ParseMessage (transport framing layer).
	MaxMessageBytes = 4 << 20

	// MaxSchemaBytes bounds one tool inputSchema serialized to JSON. It
	// protects registration-time memory and validation walks. Oversized
	// schemas skip the tool; they are never truncated (truncating JSON
	// could forge a permissive schema). Enforced in ValidateRemoteTool.
	MaxSchemaBytes = 64 * 1024

	// MaxSchemaDepth bounds inputSchema nesting depth. It protects the
	// validation walk from excessive CPU use. Enforced in ValidateRemoteTool.
	MaxSchemaDepth = 5

	// MaxSchemaProperties bounds entries in one inputSchema properties
	// object. It bounds definition size sent to providers. Enforced in
	// ValidateRemoteTool.
	MaxSchemaProperties = 64

	// MaxSchemaEnumEntries bounds entries in one inputSchema enum array.
	// It bounds validation work per property. Enforced in ValidateRemoteTool.
	MaxSchemaEnumEntries = 32

	// MaxSchemaStringBytes bounds one string leaf inside an inputSchema
	// (for example an enum entry). It keeps single values from dominating
	// the schema budget. Enforced in ValidateRemoteTool.
	MaxSchemaStringBytes = 4096

	// MaxDescriptionBytes bounds one tool description in bytes. Longer
	// descriptions are truncated as plain text on a rune boundary with a
	// marker; they are never re-serialized as JSON. It prevents metadata
	// from dominating model context. Enforced in TruncateDescription.
	MaxDescriptionBytes = 4096

	// MaxRemoteToolNameLen bounds one server-advertised tool name before
	// sanitization. Over-long names are rejected, never silently cut
	// (cutting could create ambiguity). Enforced in SanitizeRemoteName.
	MaxRemoteToolNameLen = 128

	// MaxQualifiedToolNameLen bounds a namespaced mcp__server__tool name.
	// It keeps names within provider function-name grammars. Over-long
	// qualified names are rejected. Enforced in QualifiedName.
	MaxQualifiedToolNameLen = 64

	// MaxToolsPerServer bounds discovered tools accepted from one server.
	// It bounds fullManager growth and discovery work. Enforced during
	// discovery (later phase); represented here so pagination and
	// aggregation code share one budget.
	MaxToolsPerServer = 128

	// MaxListPages bounds tools/list pagination rounds per server. It caps
	// discovery work against servers that never stop emitting cursors.
	// Enforced during discovery (later phase); represented here.
	MaxListPages = 10

	// MaxIDStringLen bounds a JSON-RPC request/response identifier in
	// string form. It keeps the pending-request map keys sane. Enforced in
	// NormalizeID.
	MaxIDStringLen = 256

	// MaxErrorDetailRunes bounds attacker-controlled excerpts embedded in
	// returned errors. Errors name the server, tool, and reason; they never
	// carry payloads. Enforced in BoundedDetail.
	MaxErrorDetailRunes = 512

	// TruncationMarker marks model-visible plain-text truncation. It is
	// appended after a rune-safe cut; it carries no executable meaning.
	TruncationMarker = "[...truncated]"
)
