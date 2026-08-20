package permissions

import "context"

// Request identifies the tool invocation being approved or rejected.
type Request struct {
	Tool      string
	Arguments map[string]any
}

// Prompt is the user's answer to an interactive permission prompt. It's a
// distinct type from Decision because "yes"/"no" answers apply only to
// the current invocation, while "always allow"/"always deny" also update
// the persisted rule set - Decision alone can't distinguish those.
type Prompt int

const (
	// PromptDenyOnce denies just this invocation ("n").
	PromptDenyOnce Prompt = iota
	// PromptAllowOnce allows just this invocation ("y").
	PromptAllowOnce
	// PromptAlwaysAllow allows this invocation and every future one ("a").
	PromptAlwaysAllow
	// PromptAlwaysDeny denies this invocation and every future one ("d").
	PromptAlwaysDeny
)

func (p Prompt) Decision() Decision {
	switch p {
	case PromptAllowOnce, PromptAlwaysAllow:
		return Allow
	default:
		return Deny
	}
}

func (p Prompt) Persist() bool {
	return p == PromptAlwaysAllow || p == PromptAlwaysDeny
}

// Asker interactively asks a human whether a tool call should proceed. It
// is the only piece of the "ask" flow that knows about a particular UI
// (terminal stdin, the TUI's modal, a future GUI, ...); everything else in
// this package and in the scheduler is UI-agnostic.
type Asker interface {
	Ask(ctx context.Context, req Request) (Prompt, error)
}

// AskerFunc adapts a plain function to the Asker interface.
type AskerFunc func(ctx context.Context, req Request) (Prompt, error)

func (f AskerFunc) Ask(ctx context.Context, req Request) (Prompt, error) {
	return f(ctx, req)
}
