package tui

import (
	"fmt"

	"forcefield/internal/command"
	"forcefield/internal/command/builtin"
	"forcefield/internal/session"
)

// newRegistry builds the set of slash commands available in the interactive chat.
func newRegistry() *command.Registry {
	reg := command.NewRegistry()
	reg.Register(builtin.NewExit())
	reg.Register(builtin.NewClear())
	reg.Register(builtin.NewModel())
	reg.Register(builtin.NewProvider())
	reg.Register(builtin.NewHelp(reg))
	reg.Register(builtin.NewSessions())
	return reg
}

// The methods below satisfy command.Context, which is the only thing
// slash commands know about the session. They translate that narrow
// interface into real changes to the transcript and the runtime, so
// commands themselves never import Bubble Tea or forcefield/internal/runtime.

// Println appends a formatted system-style line to the transcript.
func (m *model) Println(format string, args ...any) {
	m.entries = append(m.entries, chatEntry{
		Role:    roleSystem,
		Content: fmt.Sprintf(format, args...),
	})
}

// Clear empties the transcript, e.g. for /clear.
func (m *model) Clear() {
	m.entries = nil
}

// Quit marks the session to end; handleKey turns this into a tea.Quit.
func (m *model) Quit() {
	m.quitting = true
}

// Model reports the currently active model name.
func (m *model) Model() string { return m.modelName }

// Provider reports the currently active provider name.
func (m *model) Provider() string { return m.providerName }

// SetModel switches the runtime to a new model and, only once that
// succeeds, updates the label shown in the header.
func (m *model) SetModel(name string) error {
	if err := m.runtime.SetModel(name); err != nil {
		return err
	}
	m.modelName = name
	return nil
}

// SetProvider switches the runtime to a new provider and, only once that
// succeeds, updates the label shown in the header.
func (m *model) SetProvider(name string) error {
	if err := m.runtime.SetProvider(name); err != nil {
		return err
	}
	m.providerName = name
	return nil
}

// OpenSessionPicker opens the /sessions modal over sessions, which the
// caller already loaded from disk. It never reads sessions itself.
func (m *model) OpenSessionPicker(sessions []session.Session) {
	m.picker = newSessionPicker(sessions, m.session.ID)
}
