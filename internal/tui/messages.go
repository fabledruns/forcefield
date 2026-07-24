package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/runtime"
)

type responseMsg string
type errMsg struct{ err error }

func (e errMsg) Error() string { return e.err.Error() }

// runTask wraps the existing runtime.Run in a tea.Cmd. Bubble Tea always
// executes commands on their own goroutine, which is what keeps the UI
// responsive, spinner animating, input still focused, while the
// underlying HTTP call to the model provider is in flight.
//
// This is the only place the TUI talks to the rest of the application:
// one call to runtime.Run per submitted message, identical to what `ff
// run` does for a single task. No conversation history is threaded
// through here, because runtime.Run doesn't support any, each message
// is answered independently, same as the one-shot command.
func runTask(task string) tea.Cmd {
	return func() tea.Msg {
		response, err := runtime.Run(task)
		if err != nil {
			return errMsg{err}
		}
		return responseMsg(response)
	}
}