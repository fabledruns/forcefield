package tui

import (
	"forcefield/internal/providers"

	tea "github.com/charmbracelet/bubbletea"
)

type streamChunkMsg struct{ Text string }
type streamDoneMsg struct{}
type streamErrMsg struct{ err error }

func waitForChunk(stream <-chan providers.StreamEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-stream
		if !ok {
			return streamDoneMsg{}
		}

		if event.Err != nil {
			return streamErrMsg{err: event.Err}
		}

		return streamChunkMsg{
			Text: event.Text,
		}
	}
}