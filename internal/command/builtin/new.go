package builtin

import (
	"fmt"

	"forcefield/internal/command"
)

// NewSession creates a fresh persisted conversation through the interactive
// context. Session ownership stays with the TUI/session layer; this command
// only provides parsing, help, and dispatch like the other slash commands.
type NewSession struct{}

func NewNewSession() *NewSession { return &NewSession{} }

func (NewSession) Name() string        { return "new" }
func (NewSession) Aliases() []string   { return nil }
func (NewSession) Description() string { return "Start a new chat session." }
func (NewSession) Usage() string       { return "/new" }

func (NewSession) Execute(ctx command.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: /new")
	}
	return ctx.NewSession()
}
