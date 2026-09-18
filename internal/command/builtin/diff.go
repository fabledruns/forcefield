package builtin

import (
	"fmt"

	"forcefield/internal/command"
)

// Diff is the /diff command. It shows the workspace's unstaged diff
// without invoking the model.
type Diff struct{}

// NewDiff returns a ready-to-register /diff command.
func NewDiff() *Diff { return &Diff{} }

func (Diff) Name() string        { return "diff" }
func (Diff) Aliases() []string   { return nil }
func (Diff) Description() string { return "Show the workspace's unstaged diff." }
func (Diff) Usage() string       { return "/diff [path]" }

func (Diff) Execute(ctx command.Context, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: /diff [path]")
	}
	path := ""
	if len(args) == 1 {
		path = args[0]
	}
	out, err := ctx.Git("diff", path)
	if err != nil {
		return err
	}
	ctx.Println("%s", out)
	return nil
}
