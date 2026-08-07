package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/permissions"
)

// tuiAsker resolves "ask" permission decisions by handing them to the
// running bubbletea program as a message and blocking until the user
// answers via the permission modal (see permission.go). It runs on the
// scheduler's goroutine, never on the UI goroutine, so blocking here is
// safe: it doesn't stall rendering, only the one tool call awaiting an
// answer.
//
// program is set after the tea.Program is constructed (see Start in
// tui.go), since the program can't exist until the model - which needs
// this asker wired into its runtime - already does.
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
