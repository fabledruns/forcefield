package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/perfmark"
	"forcefield/internal/session"
)

func Start(sess *session.Session) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	perfmark.Event("config-loaded")

	asker := &tuiAsker{}
	// TUI-first: render the first frame from the minimal startup model
	// immediately; the runtime (skills, memory, provider, tools, agents,
	// session alignment) initializes in the background via Init and
	// installs itself through runtimeReadyMsg.
	m := newStartingModel(cfg, sess, asker)

	// notify routes background results (model discovery) back into the
	// running event loop. The program variable is captured by reference
	// and assigned before Run starts, so Send is never called on a nil
	// program from work triggered after startup.
	var program *tea.Program
	m.notify = func(msg tea.Msg) {
		if program != nil {
			program.Send(msg)
		}
	}

	program = tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	asker.program = program

	_, err = program.Run()
	return err
}
