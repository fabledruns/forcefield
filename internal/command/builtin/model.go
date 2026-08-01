package builtin

import (
	"fmt"

	"forcefield/internal/command"
	"forcefield/internal/providers"
)

// Model is the /model command. With no arguments it reports the active
// model; with one argument it switches to it.
type Model struct{}

// NewModel returns a ready-to-register /model command.
func NewModel() *Model { return &Model{} }

func (Model) Name() string        { return "model" }
func (Model) Aliases() []string   { return nil }
func (Model) Description() string { return "Show or switch the active model." }
func (Model) Usage() string       { return "/model [name]" }

func (Model) Execute(ctx command.Context, args []string) error {
	if len(args) == 0 {
		ctx.OpenModelPicker()
		return nil
	}
	if len(args) > 1 {
		return fmt.Errorf("expected exactly one model name, got %d", len(args))
	}

	name := args[0]
	if err := ctx.SetModel(name); err != nil {
		return fmt.Errorf("switch model: %w", err)
	}
	ctx.Println("✓ Model: %s", providers.ModelDisplayName(ctx.Provider(), name))
	return nil
}