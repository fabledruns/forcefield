package builtin

import (
	"fmt"

	"forcefield/internal/command"
)

// Cancel is the /cancel command. It cancels the current run through the
// same teardown as Ctrl+C.
type Cancel struct{}

// NewCancel returns a ready-to-register /cancel command.
func NewCancel() *Cancel { return &Cancel{} }

func (Cancel) Name() string        { return "cancel" }
func (Cancel) Aliases() []string   { return nil }
func (Cancel) Description() string { return "Cancel the current run." }
func (Cancel) Usage() string       { return "/cancel" }

func (Cancel) Execute(ctx command.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: /cancel")
	}
	if !ctx.CancelRun() {
		ctx.Println("No active run.")
	}
	return nil
}
