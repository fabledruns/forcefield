package builtin

import "forcefield/internal/command"

// Exit is the /exit command (aliased to /quit). It ends the interactive
// session.
type Exit struct{}

// NewExit returns a ready-to-register /exit command.
func NewExit() *Exit { return &Exit{} }

func (Exit) Name() string        { return "exit" }
func (Exit) Aliases() []string   { return []string{"quit"} }
func (Exit) Description() string { return "End the chat session." }
func (Exit) Usage() string       { return "/exit" }

func (Exit) Execute(ctx command.Context, _ []string) error {
	ctx.Quit()
	return nil
}
