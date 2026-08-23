package builtin

import (
	"fmt"

	"forcefield/internal/command"
)

// Status is the /status command. It reports what the agent currently has
// available - provider, model, session size, tool count - so users can
// see their context state without guessing.
type Status struct{}

// NewStatus returns a ready-to-register /status command.
func NewStatus() *Status { return &Status{} }

func (Status) Name() string        { return "status" }
func (Status) Aliases() []string   { return nil }
func (Status) Description() string { return "Show the active model, session size, and tools." }
func (Status) Usage() string       { return "/status" }

func (Status) Execute(ctx command.Context, _ []string) error {
	stats := ctx.SessionStats()

	ctx.Println("Provider:  %s", ctx.Provider())
	ctx.Println("Model:     %s", ctx.Model())

	if stats.ID == "" {
		ctx.Println("Session:   (none)")
	} else {
		ctx.Println("Session:   %s", stats.ID)
	}
	ctx.Println("Messages:  %d (~%s of context)", stats.Messages, humanBytes(stats.Chars))

	if tools := ctx.Tools(); len(tools) > 0 {
		ctx.Println("Tools:     %d available (/tools to list)", len(tools))
	} else {
		ctx.Println("Tools:     none")
	}
	return nil
}

// humanBytes renders a byte count as a compact human-readable size
// ("812 B", "4.2 KB", "1.3 MB").
func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
