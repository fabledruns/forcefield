// Package orchevents defines the stable JSONL event stream that records
// orchestration and headless-run evidence beside a session file
// (.forcefield/sessions/<session-id>.events.jsonl).
//
// The schema is a stable contract for scripts, acceptance tests, and the
// TUI's future replay needs: it names what happened (type), who was
// involved (run, child_run, agent chain, tool call), and bounded
// counters (turns, tool_calls, denied). It deliberately excludes task
// text, tool arguments, provider responses, and anything secret-shaped:
// correlation never requires content. See docs/Events.md.
package orchevents

import "forcefield/internal/redact"

// Version is the schema version stamped on every event. Field removals
// or meaning changes bump it; additive fields with omitempty do not.
const Version = 1

// Type is the event kind. The full set below is a closed, documented
// set; readers must tolerate unknown types so newer writers stay
// readable by older tooling.
type Type string

const (
	RunStarted          Type = "run_started"
	RunCompleted        Type = "run_completed"
	RunFailed           Type = "run_failed"
	RunCancelled        Type = "run_cancelled"
	RunBlocked          Type = "run_blocked"
	DelegationRequested Type = "delegation_requested"
	DelegationAccepted  Type = "delegation_accepted"
	DelegationRejected  Type = "delegation_rejected"
	AgentSpawned        Type = "agent_spawned"
	ChildProgress       Type = "child_progress"
	ChildCompleted      Type = "child_completed"
	ToolStarted         Type = "tool_started"
	ToolCompleted       Type = "tool_completed"
	ToolFailed          Type = "tool_failed"
	ToolDenied          Type = "tool_denied"
	ToolCancelled       Type = "tool_cancelled"
	BudgetExhausted     Type = "budget_exhausted"
	EventsTruncated     Type = "events_truncated"
)

// Budget names which limit produced a DelegationRejected or
// BudgetExhausted event. Closed set; unknown budgets are reader errors.
const (
	BudgetSpawn      = "spawn"       // max_children_per_run (cumulative)
	BudgetDepth      = "depth"       // max_depth
	BudgetTaskChars  = "task_chars"  // max_task_chars
	BudgetTotalTurns = "total_turns" // max_total_child_turns (aggregate)
)

// Event is one line of the JSONL stream. Only populated fields are
// serialized (omitempty). Seq and Ts (and RunID when left empty) are
// assigned by the Writer; producers fill identity and semantics.
type Event struct {
	Seq   int64  `json:"seq"`
	Ts    string `json:"ts"`
	RunID string `json:"run,omitempty"`
	Type  Type   `json:"type"`
	Agent string `json:"agent,omitempty"`
	Chain string `json:"chain,omitempty"`
	Depth int    `json:"depth,omitempty"`
	// ChildRunID is the fresh UUID of one child run; Chain carries the
	// delegation path ("root > child > ...") that correlates it.
	ChildRunID  string         `json:"child_run,omitempty"`
	CallID      string         `json:"call_id,omitempty"`
	Tool        string         `json:"tool,omitempty"`
	Status      string         `json:"status,omitempty"`
	Outcome     string         `json:"outcome,omitempty"` // done|error|blocked|cancelled
	Turns       int            `json:"turns,omitempty"`
	ToolCalls   int            `json:"tool_calls,omitempty"`
	Denied      int            `json:"denied,omitempty"`
	Failed      int            `json:"failed,omitempty"`
	TaskChars   int            `json:"task_chars,omitempty"`
	DurationMS  int64          `json:"duration_ms,omitempty"`
	Attempt     int            `json:"attempt,omitempty"`
	ExitCode    *int           `json:"exit_code,omitempty"`
	Error       string         `json:"error,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Budget      string         `json:"budget,omitempty"`
	BudgetLimit int            `json:"budget_limit,omitempty"`
	BudgetValue int            `json:"budget_value,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

// terminalOutcome validates the Outcome vocabulary; empty is allowed
// (not every event carries an outcome) and only the four documented
// values pass. This bounds what recovery tooling may rely on.
func validOutcome(s string) bool {
	switch s {
	case "", "done", "error", "blocked", "cancelled":
		return true
	}
	return false
}

// Terminal outcomes are constants so writers share one spelling.
const (
	OutcomeDone      = "done"
	OutcomeError     = "error"
	OutcomeBlocked   = "blocked"
	OutcomeCancelled = "cancelled"
)

// Sink receives events from the runtime's orchestrator. Emissions are
// best-effort observability: an implementer must never fail (or slow
// meaningfully) the run it observes. The package-level interface keeps
// runtime depending on this schema without a concrete file dependency.
type Sink interface {
	Emit(Event)
}

// sanitize scrubs free-text fields defensively before persistence: even
// though producers are trusted internal code, error strings can embed
// tool output. Counters and identifiers carry no prose and pass through.
func sanitize(e *Event) {
	e.Error = redact.Scrub(e.Error)
	e.Reason = redact.Scrub(e.Reason)
	e.Status = redact.Scrub(e.Status)
}

// valid reports whether an event carries the minimum identity for its
// declared type. Unknown types are allowed (forward compatibility).
func (e Event) valid() bool {
	switch e.Type {
	case "":
		return false
	case RunStarted, RunCompleted, RunFailed, RunCancelled, RunBlocked,
		DelegationRequested, DelegationAccepted, DelegationRejected,
		AgentSpawned, ChildProgress, ChildCompleted, ToolStarted,
		ToolCompleted, ToolFailed, ToolDenied, ToolCancelled,
		BudgetExhausted, EventsTruncated:
	default:
		// Unknown: forward compatibility, but still needs a type string.
	}
	if e.ChildRunID != "" && e.Chain == "" {
		// Child events must stay attributable without joining anything.
		return false
	}
	return validOutcome(e.Outcome)
}
