package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/command"
	"forcefield/internal/command/builtin"
	"forcefield/internal/providers"
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

// Clear empties the transcript, e.g. for /clear. Any live stream is
// cancelled and its per-entry bookkeeping reset, so in-flight tool events
// can't index into the rebuilt transcript and update the wrong line.
func (m *model) Clear() {
	m.stopStream(false)
	m.entries = nil
	m.activeTools = make(map[string]int)
	m.assistantBuffer = ""
	m.status = ""
	m.permissionPrompt = nil
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

// OpenProviderPicker opens the /provider modal.
func (m *model) OpenProviderPicker() {
	m.selectPicker = newProviderPicker(m.providerName)
}

// OpenModelPicker opens the /model modal, scoped to the currently
// active provider.
func (m *model) OpenModelPicker() {
	m.selectPicker = newModelPicker(m.providerName, m.modelName)
}

// chooseProvider switches to the provider with the given ID and prints
// a confirmation. Unless the new provider has zero or more than one
// known model, it also opens the model picker automatically, so
// picking a provider never leaves the user stuck on a stale model.
func (m model) chooseProvider(id string) (tea.Model, tea.Cmd) {
	if err := m.SetProvider(id); err != nil {
		m.entries = append(m.entries, chatEntry{Role: roleError, Content: err.Error()})
		m.refreshTranscript()
		return m, nil
	}
	m.Println("✓ Provider: %s", providers.DisplayName(id))

	if p, ok := providers.ByID(id); ok {
		switch len(p.Models) {
		case 0:
			// No known models for this provider; leave the model as-is.
		case 1:
			return m.chooseModel(p.Models[0].ID)
		default:
			m.selectPicker = newModelPicker(id, m.modelName)
		}
	}

	m.refreshTranscript()
	return m, nil
}

// chooseModel switches to the model with the given ID and prints a
// confirmation.
func (m model) chooseModel(id string) (tea.Model, tea.Cmd) {
	if err := m.SetModel(id); err != nil {
		m.entries = append(m.entries, chatEntry{Role: roleError, Content: err.Error()})
		m.refreshTranscript()
		return m, nil
	}
	m.Println("✓ Model: %s", providers.ModelDisplayName(m.providerName, id))
	m.refreshTranscript()
	return m, nil
}