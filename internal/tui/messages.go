package tui

import (
	"time"

	"forcefield/internal/orchevents"
	"forcefield/internal/runtime"

	tea "github.com/charmbracelet/bubbletea"
)

// orchDropSnapshot is a point-in-time copy of the sink's cumulative
// drop counters, captured when the pump extracts a batch. Counters are
// cumulative and the model diffs against its last observation, so
// reporting is O(1) per delivery with no extra queue or accumulation.
type orchDropSnapshot struct {
	droppable int // progress/tool events shed under queue pressure
	hard      int // lifecycle events lost (exhausted bound; ~never)
}

// orchDeliverMsg carries one coalesced batch of orchestration events
// from the TUI sink's pump goroutine (tuiOrchSink) into Update. gen is
// the orchestration session generation stamped onto each event at Emit
// time; the Update handler compares it to the model's live orchGen so
// batches queued before a /new, /clear, or session switch are dropped
// instead of contaminating the new session's state. The sink never
// changes identity, so no re-attach can duplicate delivery.
type orchDeliverMsg struct {
	gen    uint64
	events []orchevents.Event
	// drops snapshots the sink's cumulative drop counters at
	// extraction time, so lifecycle-event loss stays observable
	// without creating another unbounded channel (see §5 of the plan).
	drops orchDropSnapshot
}

// openingTickMsg is one frame of the opening animation. Its timestamp
// is the frame's "now": the animation state is a pure function of
// elapsed wall time (opening_anim.go), so production ticks carry
// tea.Tick's clock while tests pass synthetic timestamps and never
// sleep.
type openingTickMsg time.Time

// openingTickCmd schedules the next opening-animation frame at the
// existing openingTickInterval (33ms, PLAN §9). tea.Tick is Bubble
// Tea's standard one-shot scheduler: no sleep, no goroutine of our own.
func openingTickCmd() tea.Cmd {
	return tea.Tick(openingTickInterval, func(t time.Time) tea.Msg {
		return openingTickMsg(t)
	})
}

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
type streamCancelledMsg struct {
	err error
	gen uint64
}
type streamBlockedMsg struct {
	err error
	gen uint64
}

func waitForChunk(stream <-chan runtime.Event, gen uint64) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-stream
		if !ok {
			return streamDoneMsg{gen: gen}
		}

		if event.Type == runtime.EventCancelled {
			return streamCancelledMsg{err: event.Err, gen: gen}
		}
		if event.Type == runtime.EventBlocked {
			return streamBlockedMsg{err: event.Err, gen: gen}
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
