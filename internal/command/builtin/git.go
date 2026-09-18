package builtin

import (
	"fmt"

	"forcefield/internal/command"
)

// Git is the /git command. It shows git status for the workspace without
// invoking the model.
type Git struct{}

// NewGit returns a ready-to-register /git command.
func NewGit() *Git { return &Git{} }

func (Git) Name() string        { return "git" }
func (Git) Aliases() []string   { return nil }
func (Git) Description() string { return "Show git status for the workspace." }
func (Git) Usage() string       { return "/git" }

func (Git) Execute(ctx command.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: /git")
	}
	out, err := ctx.Git("status", "")
	if err != nil {
		return err
	}
	ctx.Println("%s", out)
	return nil
}
