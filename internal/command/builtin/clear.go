package builtin

import "forcefield/internal/command"

// Clear is the /clear command. It empties the visible transcript without
// affecting model or provider state.
type Clear struct{}

// NewClear returns a ready-to-register /clear command.
func NewClear() *Clear { return &Clear{} }

func (Clear) Name() string        { return "clear" }
func (Clear) Aliases() []string   { return nil }
func (Clear) Description() string { return "Clear the chat transcript." }
func (Clear) Usage() string       { return "/clear" }

func (Clear) Execute(ctx command.Context, _ []string) error {
	ctx.Clear()
	return nil
}
