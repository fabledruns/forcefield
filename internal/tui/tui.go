package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/session"
)

func Start(sess *session.Session) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	asker := &tuiAsker{}
	m, err := newModel(cfg, sess, asker)
	if err != nil {
		return err
	}

	program := tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	asker.program = program

	_, err = program.Run()
	return err
}
