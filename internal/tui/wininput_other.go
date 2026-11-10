//go:build !windows

package tui

import tea "github.com/charmbracelet/bubbletea"

// setupPlatformInput is a no-op on non-Windows platforms. Bubble Tea uses its
// standard Unix terminal driver which already natively supports bracketed paste
// and UTF-8 ANSI stream decoding.
func setupPlatformInput(opts *[]tea.ProgramOption, programRef **tea.Program) (func(), error) {
	return nil, nil
}
