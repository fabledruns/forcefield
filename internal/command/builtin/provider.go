package builtin

import (
	"fmt"

	"forcefield/internal/command"
	"forcefield/internal/providers"
)

// Provider is the /provider command. With no arguments it reports the
// active provider; with one argument it switches to it.
type Provider struct{}

// NewProvider returns a ready-to-register /provider command.
func NewProvider() *Provider { return &Provider{} }

func (Provider) Name() string        { return "provider" }
func (Provider) Aliases() []string   { return nil }
func (Provider) Description() string { return "Show or switch the active model provider." }
func (Provider) Usage() string       { return "/provider [name]" }

func (Provider) Execute(ctx command.Context, args []string) error {
	if len(args) == 0 {
		ctx.OpenProviderPicker()
		return nil
	}
	if len(args) > 1 {
		return fmt.Errorf("expected exactly one provider name, got %d", len(args))
	}

	name := args[0]
	if err := ctx.SetProvider(name); err != nil {
		return fmt.Errorf("switch provider: %w", err)
	}
	ctx.Println("✓ Provider: %s", providers.DisplayName(name))
	return nil
}
