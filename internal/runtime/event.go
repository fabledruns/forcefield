package runtime

import (
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/task"
)

// EventType describes an observable step in an agent run.
type EventType int

const (
	EventText EventType = iota
	EventThinking
	EventToolStart
	EventToolProgress
	EventToolFinish
	EventToolFailed
	EventToolCancelled
	EventToolDenied
	EventDone
	EventError
	// EventBlocked is emitted instead of EventDone when the runtime stops
	// a task itself - an iteration/tool-call/failure limit was hit - as
	// opposed to the model deciding it's finished.
	EventBlocked
)

// Status mirrors task.Status for convenience so callers of this package
// don't need to import internal/task just to read Event.Status.
type Status = task.Status

const (
	StatusInProgress = task.StatusInProgress
	StatusVerified   = task.StatusVerified
	StatusPartial    = task.StatusPartial
	StatusBlocked    = task.StatusBlocked
	StatusFailed     = task.StatusFailed
)

// Backwards-compatible names for the initial streaming tool-call API.
const (
	EventToolCall   = EventToolStart
	EventToolResult = EventToolFinish
)

// ToolResult describes the outcome of one tool execution.
type ToolResult struct {
	ToolCallID string
	Name       string
	Arguments  map[string]any
	Content    string
	IsError    bool
	Success    bool
	Duration   time.Duration
	Attempt    int // 1-based retry attempt that produced this result
	Err        error

	// Structured output passthrough for tools that provide it (e.g. the
	// shell tool's separate stdout/stderr streams and exit code), so
	// consumers like the TUI's expanded tool view can show them without
	// parsing Content.
	Stdout      string
	Stderr      string
	ExitCode    int
	HasExitCode bool
}

// ToolProgress describes a single live output chunk from a running tool,
// e.g. one line of shell stdout/stderr.
type ToolProgress struct {
	ToolCallID string
	Name       string
	Stream     string // "stdout", "stderr", or "progress"
	Data       string
}

// Event is emitted by StreamChat as the shared runtime loop progresses.
// EventDone contains the final model response. EventError contains the error
// that stopped the run. Multiple ToolStart/ToolProgress/ToolFinish events
// may be in flight concurrently (correlated by ToolCallID) when the
// scheduler runs independent tool calls in parallel.
type Event struct {
	Type         EventType
	Text         string
	Thinking     string
	ToolCall     *providers.ToolCall
	ToolProgress *ToolProgress
	ToolResult   *ToolResult
	Response     *providers.Response
	Status       Status
	TaskState    *task.Snapshot
	Err          error
}
