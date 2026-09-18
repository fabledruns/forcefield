// Package command implements Forcefield's TUI-independent slash commands.
package command

import (
	"forcefield/internal/providers"
	"forcefield/internal/session"
	"forcefield/internal/skills"
)

// SessionStats summarizes the active conversation without exposing a
// session.Session (and its mutation methods) to commands.
type SessionStats struct {
	// ID is the active session's identifier.
	ID string
	// Messages is how many messages the conversation holds.
	Messages int
	// Chars is the total size of all message contents combined - an
	// honest, provider-independent lower bound on context growth.
	Chars int
	// SaveError is the active session's most recent save failure, empty
	// when the last save succeeded. Commands surface it; they never set
	// it.
	SaveError string
	// PlanStatus is the accepted plan's status (draft, building, done,
	// partial), empty when the session has no plan.
	PlanStatus string
}

// AgentSummary describes one specialised agent for listings.
type AgentSummary struct {
	Name        string
	Description string
	Tools       []string
	// Skills lists assigned skill IDs; AllSkills reports full-catalog access.
	Skills    []string
	AllSkills bool
}

// ContextInfo summarizes estimated context consumption for /usage and
// /context. EstTokens is a deterministic local estimate (never billed
// usage); Limit <= 0 means the model's window is unknown, in which case
// only MaxMessages bounds the turn. Kept/Evicted describe the turn-window
// selection; Summarize reports whether evicted turns become a digest.
type ContextInfo struct {
	Messages    int
	Chars       int
	EstTokens   int
	Limit       int
	Reserve     int
	MaxMessages int
	Kept        int
	Evicted     int
	Summarize   bool
	// Compacted counts messages dropped by size-bound compaction across
	// the session lifetime.
	Compacted int
}

// JobSnapshot is a race-free view of one background shell job for /jobs.
type JobSnapshot struct {
	ID       string
	Command  string
	State    string
	HasExit  bool
	ExitCode int
}

// Context is the session-facing interface used by commands.
type Context interface {
	Println(format string, args ...any)
	Clear()
	// NewSession persists and replaces the active conversation.
	NewSession() error
	Quit()
	Model() string
	Provider() string
	SetModel(name string) error
	SetProvider(name string) error
	OpenSessionPicker(sessions []session.Session)
	OpenProviderPicker()
	OpenModelPicker()
	// SessionStats describes the active conversation.
	SessionStats() SessionStats
	// ContextInfo describes estimated context consumption.
	ContextInfo() ContextInfo
	// Tools returns one human-readable line per available tool, e.g.
	// "read_file: Read the contents of a file.".
	Tools() []string
	// Git runs a read-only git inspection (status, diff, staged, log,
	// changed) scoped to path inside the workspace. It reports the
	// tool's soft errors as Go errors.
	Git(action, path string) (string, error)
	// Jobs snapshots every remembered background shell job, oldest first.
	Jobs() []JobSnapshot
	// CancelRun cancels the current run, reporting whether one was active.
	CancelRun() bool
	// StartPlan begins a read-only planning turn for task. It never
	// modifies the workspace.
	StartPlan(task string) error
	// StartBuild executes the accepted plan through the normal agent
	// loop. It reports an error when no plan exists.
	StartBuild() error
	// ReasoningCapabilities returns the capability for the active model.
	ReasoningCapabilities() providers.ReasoningCapabilities
	// Effort reports the current effort level for the active model.
	Effort() string
	// SetEffort validates and stores the effort level for the active model.
	SetEffort(level string) error
	// Thinking returns the current thinking config for the active model.
	Thinking() *providers.ThinkingConfig
	// SetThinking validates and stores the thinking config for the active model.
	SetThinking(cfg providers.ThinkingConfig) error
	// ToggleThinking flips boolean thinking for the active model.
	ToggleThinking() (bool, error)
	// Skills returns the current skill catalog in display order.
	Skills() []skills.Skill
	// LoadSkill returns the Markdown body for a skill id. It reports
	// ErrSkillNotFound when the id is unknown.
	LoadSkill(id string) (string, error)
	// Agent reports the active agent name.
	Agent() string
	// SetAgent switches the active agent.
	SetAgent(name string) error
	// Agents returns summaries for all known agents.
	Agents() []AgentSummary
}

// Command is a slash command.
type Command interface {
	Name() string
	Aliases() []string
	Description() string
	Usage() string
	Execute(ctx Context, args []string) error
}
