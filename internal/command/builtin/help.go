package builtin

import (
	"fmt"
	"strings"

	"forcefield/internal/command"
)

// Help is the /help command (aliased to /?). It lists every command
// registered in reg, so it never goes stale as commands are added or
// removed — there is nothing to update by hand.
type Help struct {
	reg *command.Registry
}

// NewHelp returns a ready-to-register /help command that lists whatever
// is registered in reg at the time /help runs. reg is typically the same
// Registry that Help itself is about to be registered into: construct
// the Registry first, then construct Help with it, then Register(help).
func NewHelp(reg *command.Registry) *Help {
	return &Help{reg: reg}
}

func (Help) Name() string        { return "help" }
func (Help) Aliases() []string   { return []string{"?"} }
func (Help) Description() string { return "List available commands." }
func (Help) Usage() string       { return "/help" }

func (h *Help) Execute(ctx command.Context, _ []string) error {
	var b strings.Builder
	b.WriteString("Available commands:\n")

	for _, cmd := range h.reg.All() {
		fmt.Fprintf(&b, "  %-16s %s\n", cmd.Usage(), cmd.Description())
		if aliases := cmd.Aliases(); len(aliases) > 0 {
			fmt.Fprintf(&b, "  %-16s (alias: %s)\n", "", joinAliases(aliases))
		}
	}

	ctx.Println("%s", strings.TrimRight(b.String(), "\n"))
	return nil
}

func joinAliases(aliases []string) string {
	prefixed := make([]string, len(aliases))
	for i, a := range aliases {
		prefixed[i] = "/" + a
	}
	return strings.Join(prefixed, ", ")
}
