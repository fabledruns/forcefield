package tui

import (
	"forcefield/internal/runtime"

	tea "github.com/charmbracelet/bubbletea"
)

// Stream messages carry the generation of the stream they came from. The
// model drops any message whose generation no longer matches, so events
// from a stream that was replaced (session switch, /clear, quit) can never
// be appended to the wrong transcript or re-arm a reader on a dead channel.
type streamEventMsg struct {
	Event runtime.Event
	gen   uint64
}
type streamDoneMsg struct{ gen uint64 }
type streamErrMsg struct {
	err error
	gen uint64
}

func waitForChunk(stream <-chan runtime.Event, gen uint64) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-stream
		if !ok {
			return streamDoneMsg{gen: gen}
		}

		if event.Type == runtime.EventError || event.Err != nil {
			return streamErrMsg{err: event.Err, gen: gen}
		}
		if event.Type == runtime.EventDone {
			return streamDoneMsg{gen: gen}
		}

		return streamEventMsg{Event: event, gen: gen}
	}
}
