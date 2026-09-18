package builtin

import (
	"fmt"

	"forcefield/internal/command"
)

// Build is the /build command. It executes the accepted plan through the
// normal agent loop; the context reports an error when no plan exists.
type Build struct{}

// NewBuild returns a ready-to-register /build command.
func NewBuild() *Build { return &Build{} }

func (Build) Name() string        { return "build" }
func (Build) Aliases() []string   { return nil }
func (Build) Description() string { return "Execute the accepted plan." }
func (Build) Usage() string       { return "/build" }

func (Build) Execute(ctx command.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: /build")
	}
	return ctx.StartBuild()
}
