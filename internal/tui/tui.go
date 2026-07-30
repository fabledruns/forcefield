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

	program := tea.NewProgram(
		newModel(cfg, sess),
		tea.WithAltScreen(),       
		tea.WithMouseCellMotion(), 
	)

	_, err = program.Run()
	return err
}
