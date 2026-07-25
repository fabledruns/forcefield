package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
)

func Start() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	program := tea.NewProgram(
		newModel(cfg),
		tea.WithAltScreen(),       
		tea.WithMouseCellMotion(), 
	)

	_, err = program.Run()
	return err
}
