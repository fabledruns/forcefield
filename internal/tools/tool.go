// Package tools defines Forcefield's tool abstractions.
package tools

import (
	"context"
	"time"
)

// Tool represents an action the model can invoke.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	Execute(ctx context.Context, args map[string]any) (Result, error)
}

// Result contains the output of a tool execution. Content/IsError remain
// the fields every caller can rely on (what gets fed back to the model).
// The remaining fields are populated on a best-effort basis by tools that
// have something structured to report (e.g. shell) and are otherwise left
// at their zero value.
type Result struct {
	Content string
	IsError bool

	// Structured fields. Tools that don't have a meaningful value for one
	// of these simply leave it unset.
	ExitCode   int
	Stdout     string
	Stderr     string
	DurationMs int64
	Tool       string
	Command    string
	Metadata   map[string]any
}

// Definition describes a tool without exposing its implementation.
type Definition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// Permission identifies a privileged capability a tool may require.
type Permission string

const (
	PermissionReadFilesystem  Permission = "read_filesystem"
	PermissionWriteFilesystem Permission = "write_filesystem"
	PermissionExecuteShell    Permission = "execute_shell"
	PermissionNetworkAccess   Permission = "network_access"
	PermissionDeleteFiles     Permission = "delete_files"
)

// Metadata describes properties of a tool relevant to scheduling,
// permissions, and how the model should be told to use it.
type Metadata struct {
	Timeout              time.Duration
	SupportsStreaming    bool
	SupportsCancellation bool
	SupportsParallel     bool
	RequiredPermissions  []Permission
	Retryable            bool
}

// DefaultMetadata is applied to any Tool that does not implement
// MetadataProvider: safe to run in parallel, no special permissions, a
// generous default timeout, no streaming/retry behavior assumed.
var DefaultMetadata = Metadata{
	Timeout:          30 * time.Second,
	SupportsParallel: true,
}

// MetadataProvider is implemented by tools that want to advertise
// permissions, timeouts, or execution characteristics beyond the
// defaults. Tools that don't implement it get DefaultMetadata.
type MetadataProvider interface {
	Metadata() Metadata
}

// MetadataOf returns t's advertised Metadata, falling back to
// DefaultMetadata if t doesn't implement MetadataProvider.
func MetadataOf(t Tool) Metadata {
	if mp, ok := t.(MetadataProvider); ok {
		return mp.Metadata()
	}
	return DefaultMetadata
}

// StreamChunk is a single piece of live output emitted by a tool while it
// runs, e.g. a line of stdout from a shell command.
type StreamChunk struct {
	Stream string // "stdout", "stderr", or "progress"
	Data   string
}

// StreamingTool is implemented by tools that can emit output as they run
// instead of only returning a Result at the end. onChunk may be called from
// any goroutine and must not block for long.
type StreamingTool interface {
	Tool
	ExecuteStream(ctx context.Context, args map[string]any, onChunk func(StreamChunk)) (Result, error)
}
