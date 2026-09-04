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
}

// AgentSummary describes one specialised agent for listings.
type AgentSummary struct {
	Name        string
	Description string
	Tools       []string
}

// Context is the session-facing interface used by commands.
type Context interface {
	Println(format string, args ...any)
	Clear()
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
	// Tools returns one human-readable line per available tool, e.g.
	// "read_file: Read the contents of a file.".
	Tools() []string
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
