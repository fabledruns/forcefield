package builtin

import (
	"fmt"
	"strings"

	"forcefield/internal/command"
)

// Jobs is the /jobs command. It lists background shell jobs without
// invoking the model and never starts, polls, or cancels anything.
type Jobs struct{}

// NewJobs returns a ready-to-register /jobs command.
func NewJobs() *Jobs { return &Jobs{} }

func (Jobs) Name() string        { return "jobs" }
func (Jobs) Aliases() []string   { return nil }
func (Jobs) Description() string { return "List background shell jobs." }
func (Jobs) Usage() string       { return "/jobs" }

func (Jobs) Execute(ctx command.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: /jobs")
	}
	jobs := ctx.Jobs()
	if len(jobs) == 0 {
		ctx.Println("No background jobs.")
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Background jobs (%d):", len(jobs))
	for _, j := range jobs {
		line := fmt.Sprintf("  %s · %s · %s", j.ID, j.State, truncateJobCommand(j.Command))
		if j.HasExit {
			line += fmt.Sprintf(" (exit %d)", j.ExitCode)
		}
		b.WriteString("\n" + line)
	}
	ctx.Println("%s", b.String())
	return nil
}

// truncateJobCommand keeps one job row readable; commands are arbitrary
// bytes, so truncation is rune-safe.
func truncateJobCommand(s string) string {
	const max = 80
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
