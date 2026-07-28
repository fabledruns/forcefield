package tui

import (
	"forcefield/internal/runtime"

	tea "github.com/charmbracelet/bubbletea"
)

type streamEventMsg struct{ Event runtime.Event }
type streamDoneMsg struct{}
type streamErrMsg struct{ err error }

func waitForChunk(stream <-chan runtime.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-stream
		if !ok {
			return streamDoneMsg{}
		}

		if event.Type == runtime.EventError || event.Err != nil {
			return streamErrMsg{err: event.Err}
		}
		if event.Type == runtime.EventDone {
			return streamDoneMsg{}
		}

		return streamEventMsg{Event: event}
	}
}
