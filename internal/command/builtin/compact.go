package builtin

import (
	"fmt"

	"forcefield/internal/command"
)

// Compact is the /compact command. Compaction itself is automatic; this
// command only reports its state without mutating anything.
type Compact struct{}

// NewCompact returns a ready-to-register /compact command.
func NewCompact() *Compact { return &Compact{} }

func (Compact) Name() string        { return "compact" }
func (Compact) Aliases() []string   { return nil }
func (Compact) Description() string { return "Show automatic compaction state." }
func (Compact) Usage() string       { return "/compact" }

func (Compact) Execute(ctx command.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: /compact")
	}
	info := ctx.ContextInfo()
	ctx.Println("Messages:       %d", info.Messages)
	ctx.Println("Compacted:      %d lifetime", info.Compacted)
	if info.Summarize {
		ctx.Println("Summary digest: on (evicted turns become a digest)")
	} else {
		ctx.Println("Summary digest: off (evicted turns are dropped)")
	}
	return nil
}
