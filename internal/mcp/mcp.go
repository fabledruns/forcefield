// Package mcp is Forcefield's Model Context Protocol integration boundary.
//
// MCP servers are untrusted even when the user explicitly configured them.
// Every server-provided value (tool names, descriptions, schemas, results,
// errors, stderr, metadata) is treated as untrusted data: it passes through
// validation, size caps, truncation, and redaction before it can influence
// the model, the transcript, a session file, or a trace.
//
// Phase 1 scope: configuration shape and validation, JSON-RPC 2.0
// framing/parsing primitives, protocol-version negotiation primitives, and
// sanitization and size-limit enforcement for untrusted protocol data.
// Later phases add subprocess lifecycle, client execution, tool adapters,
// and runtime wiring. Nothing in this package spawns processes, performs
// I/O, or branches runtime scheduling: those remain with their existing
// owners.
package mcp
