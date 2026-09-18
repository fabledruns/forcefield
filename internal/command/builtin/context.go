package builtin

import (
	"fmt"

	"forcefield/internal/command"
)

// Context is the /context command. It shows what the next model turn will
// send without invoking the model.
type Context struct{}

// NewContext returns a ready-to-register /context command.
func NewContext() *Context { return &Context{} }

func (Context) Name() string        { return "context" }
func (Context) Aliases() []string   { return nil }
func (Context) Description() string { return "Show what the next turn will send." }
func (Context) Usage() string       { return "/context" }

func (Context) Execute(ctx command.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: /context")
	}
	info := ctx.ContextInfo()
	if info.Limit > 0 {
		ctx.Println("Window:      %d tokens (reserve %d, cap %d msgs)",
			info.Limit, info.Reserve, info.MaxMessages)
	} else {
		ctx.Println("Window:      unknown (count-bounded, cap %d msgs)", info.MaxMessages)
	}
	ctx.Println("Turn window: %d kept · %d evicted", info.Kept, info.Evicted)
	if info.Summarize {
		ctx.Println("Summary:     on (evicted turns become a digest)")
	} else {
		ctx.Println("Summary:     off")
	}
	return nil
}
