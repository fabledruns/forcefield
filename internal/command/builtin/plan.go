package builtin

import (
	"fmt"
	"strings"

	"forcefield/internal/command"
)

// Plan is the /plan command. It hands the task to a read-only planning
// turn; workspace modification happens only through a later /build.
type Plan struct{}

// NewPlan returns a ready-to-register /plan command.
func NewPlan() *Plan { return &Plan{} }

func (Plan) Name() string        { return "plan" }
func (Plan) Aliases() []string   { return nil }
func (Plan) Description() string { return "Produce an implementation plan without modifying anything." }
func (Plan) Usage() string       { return "/plan <task>" }

func (Plan) Execute(ctx command.Context, args []string) error {
	task := strings.Join(args, " ")
	if strings.TrimSpace(task) == "" {
		return fmt.Errorf("usage: /plan <task>")
	}
	return ctx.StartPlan(task)
}
