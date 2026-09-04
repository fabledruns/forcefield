package builtin

import (
	"fmt"
	"strings"

	"forcefield/internal/command"
)

// Agent is the /agent command. With no arguments it lists available agents;
// with one argument it switches to it.
type Agent struct{}

// NewAgent returns a ready-to-register /agent command.
func NewAgent() *Agent { return &Agent{} }

func (Agent) Name() string        { return "agent" }
func (Agent) Aliases() []string   { return nil }
func (Agent) Description() string { return "Show or switch the active specialised agent." }
func (Agent) Usage() string       { return "/agent [name]" }

func (Agent) Execute(ctx command.Context, args []string) error {
	if len(args) == 0 {
		agents := ctx.Agents()
		if len(agents) == 0 {
			ctx.Println("No agents are registered.")
			return nil
		}
		active := ctx.Agent()
		var lines []string
		for _, a := range agents {
			marker := " "
			if a.Name == active {
				marker = "●"
			}
			tools := strings.Join(a.Tools, ", ")
			if tools == "" {
				tools = "no tools"
			}
			lines = append(lines, fmt.Sprintf("%s %s — %s [%s]", marker, a.Name, a.Description, tools))
		}
		ctx.Println("Available agents:\n  %s", strings.Join(lines, "\n  "))
		ctx.Println("Active: %s", active)
		ctx.Println("Usage: /agent <name> to switch (e.g. /agent coding)")
		return nil
	}
	if len(args) > 1 {
		return fmt.Errorf("expected exactly one agent name, got %d", len(args))
	}
	name := strings.TrimSpace(args[0])
	if name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	prev := ctx.Agent()
	if err := ctx.SetAgent(name); err != nil {
		return fmt.Errorf("switch agent: %w", err)
	}
	// Find description for confirmation.
	desc := ""
	for _, a := range ctx.Agents() {
		if a.Name == strings.ToLower(name) {
			desc = a.Description
			break
		}
	}
	if desc != "" {
		ctx.Println("✓ Agent: %s — %s", strings.ToLower(name), desc)
	} else {
		ctx.Println("✓ Agent: %s", strings.ToLower(name))
	}
	if prev != "" && prev != strings.ToLower(name) {
		ctx.Println("  (was %s)", prev)
	}
	activeTools := ctx.Tools()
	ctx.Println("  Tools: %d available", len(activeTools))
	return nil
}
