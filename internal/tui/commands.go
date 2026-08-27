package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/command"
	"forcefield/internal/command/builtin"
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
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
	reg.Register(builtin.NewStatus())
	reg.Register(builtin.NewTools())
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

// OpenProviderPicker opens the /provider modal over every configured or
// known provider, with capabilities and availability per row.
func (m *model) OpenProviderPicker() {
	m.selectPicker = newSelectPicker("Provider", providerOptions(m.providerSummaries(), m.providerName), scopeProvider)
}

// OpenModelPicker opens the /model modal for the currently active
// provider. Models come from the runtime's catalog (active model first,
// then discovered or fallback entries). When the listing is stale,
// discovery starts immediately in the background and the picker shows a
// "Fetching models…" line until it lands.
func (m *model) OpenModelPicker() {
	m.openModelPickerFor(m.providerName)
}

// openModelPickerFor opens the model picker for one provider and kicks
// off lazy discovery when needed.
func (m *model) openModelPickerFor(providerID string) {
	current := ""
	if providerID == m.providerName {
		current = m.modelName
	}

	models, state := m.runtime.ModelCatalog(providerID)
	options := modelOptions(models, current, state)

	picker := newSelectPicker("Model", options, scopeModel)
	picker.provider = providerID
	picker.fetching = state == runtime.ModelsStale
	m.selectPicker = picker

	if picker.fetching {
		m.startDiscovery(providerID, false)
	}
}

// providerSummaries asks the runtime which providers exist right now.
// It returns an empty slice rather than panicking in tests that build
// partial models without a runtime.
func (m *model) providerSummaries() []runtime.ProviderSummary {
	if m.runtime == nil {
		return nil
	}
	return m.runtime.ProviderSummaries()
}

// SessionStats describes the active conversation for /status.
func (m *model) SessionStats() command.SessionStats {
	stats := command.SessionStats{ID: m.session.ID, Messages: len(m.session.Messages)}
	for _, msg := range m.session.Messages {
		stats.Chars += len(msg.Content)
	}
	return stats
}

// Tools returns one line per registered tool for /tools and /status.
func (m *model) Tools() []string {
	if m.runtime == nil {
		return nil
	}
	return m.runtime.ToolSummaries()
}

// chooseProvider switches to the provider with the given ID and prints
// a confirmation. Unless the new provider has exactly one known model,
// it also opens the model picker automatically, so picking a provider
// never leaves the user stuck on a stale model. A stale listing starts
// background discovery; the picker shows fallbacks plus "Fetching
// models…" until the fresh list arrives.
func (m model) chooseProvider(id string) (tea.Model, tea.Cmd) {
	if err := m.SetProvider(id); err != nil {
		m.entries = append(m.entries, chatEntry{Role: roleError, Content: err.Error()})
		m.refreshTranscript()
		return m, nil
	}
	m.Println("✓ Provider: %s", providers.DisplayName(id))

	models, state := m.runtime.ModelCatalog(id)
	switch {
	case len(models) == 0 && state == runtime.ModelsUnsupported:
		// Nothing selectable and nothing to discover; leave the model as-is.
	case len(models) == 1:
		return m.chooseModel(models[0].ID)
	default:
		picker := newSelectPicker("Model", modelOptions(models, m.modelName, state), scopeModel)
		picker.provider = id
		picker.fetching = state == runtime.ModelsStale
		m.selectPicker = picker
		if picker.fetching {
			m.startDiscovery(id, false)
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
