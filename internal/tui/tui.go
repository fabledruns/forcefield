package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
)

// Start loads config and runs the interactive chat program until the
// user quits (esc or ctrl+c). It blocks for the lifetime of the session.
func Start() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	program := tea.NewProgram(
		newModel(cfg),
		tea.WithAltScreen(),       // full-screen mode, restores the terminal on exit
		tea.WithMouseCellMotion(), // enables mouse-wheel scrolling in the transcript
	)

	_, err = program.Run()
	return err
}
