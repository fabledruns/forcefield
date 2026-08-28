package builtin

import (
	"fmt"
	"strings"

	"forcefield/internal/command"
)

// Effort is the /effort command. It controls reasoning effort for models
// that expose it.
type Effort struct{}

func NewEffort() *Effort { return &Effort{} }

func (Effort) Name() string        { return "effort" }
func (Effort) Aliases() []string   { return nil }
func (Effort) Description() string { return "Show or set reasoning effort for the active model." }
func (Effort) Usage() string       { return "/effort [level]" }

func (Effort) Execute(ctx command.Context, args []string) error {
	caps := ctx.ReasoningCapabilities()
	if !caps.SupportsEffort() {
		ctx.Println("Current model does not support configurable effort.")
		if len(args) > 0 {
			return fmt.Errorf("Current model does not support configurable effort.")
		}
		return nil
	}
	if len(args) == 0 {
		current := ctx.Effort()
		levels := strings.Join(caps.Effort.Levels, ", ")
		if current == "" {
			ctx.Println("Effort: (not set) (available: %s)", levels)
		} else {
			ctx.Println("Effort: %s (available: %s)", current, levels)
		}
		return nil
	}
	if len(args) > 1 {
		return fmt.Errorf("expected at most one effort level, got %d", len(args))
	}
	level := args[0]
	if err := ctx.SetEffort(level); err != nil {
		return err
	}
	canonical := caps.CanonicalEffort(level)
	ctx.Println("✓ Effort: %s", canonical)
	return nil
}
