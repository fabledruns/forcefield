package tui

import (
	"fmt"

	"forcefield/internal/command"
	"forcefield/internal/command/builtin"
)

// newRegistry builds the set of slash commands available in the
// interactive chat. It's called once, when the session starts (see
// newModel) — command lookup during the session is then just an O(1)
// map read inside the Registry.
//
// Adding a command means adding one line here (plus its own file under
// command/builtin); nothing else in this package needs to change.
func newRegistry() *command.Registry {
	reg := command.NewRegistry()
	reg.Register(builtin.NewExit())
	reg.Register(builtin.NewClear())
	reg.Register(builtin.NewModel())
	reg.Register(builtin.NewProvider())
	// Help needs to see the registry it's part of, so it's constructed
	// (not registered) last, after everything it should list already
	// has a home in reg.
	reg.Register(builtin.NewHelp(reg))
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
