package builtin

import (
	"strings"

	"forcefield/internal/command"
)

// Tools is the /tools command. It lists the tools the agent can call,
// so tool availability is discoverable instead of implicit.
type Tools struct{}

// NewTools returns a ready-to-register /tools command.
func NewTools() *Tools { return &Tools{} }

func (Tools) Name() string        { return "tools" }
func (Tools) Aliases() []string   { return nil }
func (Tools) Description() string { return "List the tools available to the agent." }
func (Tools) Usage() string       { return "/tools" }

func (Tools) Execute(ctx command.Context, _ []string) error {
	names := ctx.Tools()
	if len(names) == 0 {
		ctx.Println("No tools are registered.")
		return nil
	}
	ctx.Println("Available tools:\n  %s", strings.Join(names, "\n  "))
	return nil
}
