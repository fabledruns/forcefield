package builtin

import (
	"fmt"

	"forcefield/internal/command"
)

// Usage is the /usage command. It reports session size and estimated
// context consumption without invoking the model.
type Usage struct{}

// NewUsage returns a ready-to-register /usage command.
func NewUsage() *Usage { return &Usage{} }

func (Usage) Name() string        { return "usage" }
func (Usage) Aliases() []string   { return nil }
func (Usage) Description() string { return "Show session and context usage." }
func (Usage) Usage() string       { return "/usage" }

func (Usage) Execute(ctx command.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: /usage")
	}
	info := ctx.ContextInfo()
	ctx.Println("Messages:    %d (~%s)", info.Messages, humanBytes(info.Chars))
	ctx.Println("Est. tokens: ~%d", info.EstTokens)
	if info.Limit > 0 {
		ctx.Println("Budget:      %d token window (reserve %d, cap %d msgs)",
			info.Limit, info.Reserve, info.MaxMessages)
		if headroom := info.Limit - info.Reserve - info.EstTokens; headroom >= 0 {
			ctx.Println("Fit:         yes (~%d headroom)", headroom)
		} else {
			ctx.Println("Fit:         over by ~%d tokens", -headroom)
		}
	} else {
		ctx.Println("Budget:      unknown window (count-bounded, cap %d msgs)", info.MaxMessages)
	}
	return nil
}
