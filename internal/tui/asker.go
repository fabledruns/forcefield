package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/permissions"
)

// tuiAsker hands "ask" decisions to the Bubble Tea program and blocks the
// scheduler goroutine (never the UI goroutine) until answered. See
// docs/TUI.md.
type tuiAsker struct {
	program *tea.Program
}

func (a *tuiAsker) Ask(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
	respond := make(chan permissions.Prompt, 1)

	a.program.Send(permissionRequestMsg{request: req, respond: respond})

	select {
	case answer := <-respond:
		return answer, nil
	case <-ctx.Done():
		return permissions.PromptDenyOnce, ctx.Err()
	}
}
