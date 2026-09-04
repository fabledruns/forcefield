package builtin

import (
	"fmt"
	"strings"

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

	ctx.Println("Agent:     %s", ctx.Agent())
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

	caps := ctx.ReasoningCapabilities()
	if caps.SupportsEffort() {
		if lvl := ctx.Effort(); lvl != "" {
			ctx.Println("Effort:    %s (available: %s)", lvl, strings.Join(caps.Effort.Levels, ", "))
		} else {
			ctx.Println("Effort:    (not set) (available: %s)", strings.Join(caps.Effort.Levels, ", "))
		}
	}
	if caps.SupportsThinking() {
		switch caps.Thinking.Kind {
		case "bool":
			if tc := ctx.Thinking(); tc != nil && tc.Enabled != nil {
				if *tc.Enabled {
					ctx.Println("Thinking:  on")
				} else {
					ctx.Println("Thinking:  off")
				}
			} else {
				ctx.Println("Thinking:  (not set) (on/off)")
			}
		case "budget":
			if tc := ctx.Thinking(); tc != nil {
				if tc.Enabled != nil && !*tc.Enabled {
					ctx.Println("Thinking:  off")
				} else if tc.Budget != nil {
					ctx.Println("Thinking:  budget %d (range %d-%d)", *tc.Budget, caps.Thinking.MinBudget, caps.Thinking.MaxBudget)
				} else if tc.Enabled != nil && *tc.Enabled {
					ctx.Println("Thinking:  on")
				} else {
					ctx.Println("Thinking:  (not set) (budget %d-%d)", caps.Thinking.MinBudget, caps.Thinking.MaxBudget)
				}
			} else {
				ctx.Println("Thinking:  (not set) (budget %d-%d)", caps.Thinking.MinBudget, caps.Thinking.MaxBudget)
			}
		case "enum":
			if tc := ctx.Thinking(); tc != nil && tc.Level != "" {
				ctx.Println("Thinking:  %s (available: %s)", tc.Level, strings.Join(caps.Thinking.Levels, ", "))
			} else {
				ctx.Println("Thinking:  (not set) (available: %s)", strings.Join(caps.Thinking.Levels, ", "))
			}
		}
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
