package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/command"
	"forcefield/internal/command/builtin"
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
	"forcefield/internal/skills"
)

// newRegistry builds the set of slash commands available in the interactive chat.
func newRegistry() *command.Registry {
	reg := command.NewRegistry()
	reg.Register(builtin.NewExit())
	reg.Register(builtin.NewClear())
	reg.Register(builtin.NewModel())
	reg.Register(builtin.NewProvider())
	reg.Register(builtin.NewAgent())
	reg.Register(builtin.NewHelp(reg))
	reg.Register(builtin.NewSessions())
	reg.Register(builtin.NewStatus())
	reg.Register(builtin.NewTools())
	reg.Register(builtin.NewEffort())
	reg.Register(builtin.NewThinking())
	reg.Register(builtin.NewSkills())
	return reg
}

// The methods below satisfy command.Context, which is the only thing
// slash commands know about the session. They translate that narrow
// interface into real changes to the transcript and the runtime, so
// commands themselves never import Bubble Tea or forcefield/internal/runtime.

// Println appends a formatted system-style line to the transcript.
// Consecutive System messages that are part of the same logical event
// (e.g. initialization or a slash command that prints several lines)
// are coalesced into a single rendered System block so the transcript
// shows one "System" header per event instead of one per line. All
// content is preserved, joined with newlines.
func (m *model) Println(format string, args ...any) {
	content := fmt.Sprintf(format, args...)
	if len(m.entries) > 0 && m.entries[len(m.entries)-1].Role == roleSystem {
		prev := &m.entries[len(m.entries)-1]
		if prev.Content == "" {
			prev.Content = content
		} else if content == "" {
			prev.Content += "\n"
		} else {
			prev.Content += "\n" + content
		}
		return
	}
	m.entries = append(m.entries, chatEntry{
		Role:    roleSystem,
		Content: content,
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

// SetModel switches the runtime to a new model and persists the selection
// to config.yaml so it survives restarts. The header label updates once the
// in-memory switch succeeds; a persistence failure is returned instead of
// being silently ignored, while the new model stays active for this session.
func (m *model) SetModel(name string) error {
	if err := m.runtime.SetModel(name); err != nil {
		return err
	}
	m.modelName = name
	if err := m.runtime.SaveConfig(); err != nil {
		return fmt.Errorf("save model selection: %w", err)
	}
	return nil
}

// SetProvider switches the runtime to a new provider and persists the
// selection to config.yaml so it survives restarts. The header label
// updates once the in-memory switch succeeds; a persistence failure is
// returned instead of being silently ignored, while the new provider stays
// active for this session.
func (m *model) SetProvider(name string) error {
	if err := m.runtime.SetProvider(name); err != nil {
		return err
	}
	m.providerName = name
	if err := m.runtime.SaveConfig(); err != nil {
		return fmt.Errorf("save provider selection: %w", err)
	}
	return nil
}

// Agent reports the currently active agent name.
func (m *model) Agent() string {
	if m.runtime == nil {
		return m.agentName
	}
	return m.runtime.CurrentAgent()
}

// agentLabel returns the header label for the active agent, preserving a
// legacy custom agent.name when one is configured.
func (m *model) agentLabel() string {
	if m.runtime == nil {
		return m.agentName
	}
	return m.runtime.AgentDisplayName()
}

// SetAgent switches the runtime to a new agent, persisting the session's
// agent and updating the header. It cancels any active stream first so
// the switch never mutates state underneath a running turn.
func (m *model) SetAgent(name string) error {
	if m.runtime == nil {
		return fmt.Errorf("runtime not available")
	}
	// Cancel active stream before mutating agent state.
	if m.waiting || m.stream != nil {
		m.stopStream(false)
	}
	if err := m.runtime.SetAgent(name); err != nil {
		return err
	}
	m.agentName = m.agentLabel()
	// Update provider/model labels if the agent hint changed them.
	m.providerName = m.runtime.CurrentProvider()
	m.modelName = m.runtime.CurrentModel()
	// Persist the agent key (not the display label, which may preserve
	// a legacy custom agent.name) so reloads resolve deterministically.
	if m.session != nil {
		m.session.Agent = m.runtime.CurrentAgent()
		_ = m.session.Save()
	}
	return nil
}

// Agents returns summaries for all known agents.
func (m *model) Agents() []command.AgentSummary {
	if m.runtime == nil {
		return nil
	}
	out := m.runtime.AgentSummaries()
	res := make([]command.AgentSummary, 0, len(out))
	for _, a := range out {
		res = append(res, command.AgentSummary{
			Name:        a.Name,
			Description: a.Description,
			Tools:       append([]string(nil), a.Tools...),
			Skills:      append([]string(nil), a.Skills...),
			AllSkills:   a.AllSkills,
		})
	}
	return res
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

// Skills returns the global skill catalog for /skills.
func (m *model) Skills() []skills.Skill {
	if m.runtime == nil {
		return nil
	}
	return m.runtime.Skills()
}

// LoadSkill returns the Markdown body for a skill id.
func (m *model) LoadSkill(id string) (string, error) {
	if m.runtime == nil {
		return "", fmt.Errorf("runtime not available")
	}
	return m.runtime.LoadSkill(id)
}

// ReasoningCapabilities reports the active model's reasoning capabilities.
func (m *model) ReasoningCapabilities() providers.ReasoningCapabilities {
	if m.runtime == nil {
		return providers.ReasoningCapabilities{}
	}
	return m.runtime.CurrentReasoningCapabilities()
}

// Effort reports the current effort level.
func (m *model) Effort() string {
	if m.runtime == nil {
		return ""
	}
	return m.runtime.CurrentEffort()
}

// SetEffort sets the effort level for the active model.
func (m *model) SetEffort(level string) error {
	if m.runtime == nil {
		return fmt.Errorf("runtime not available")
	}
	return m.runtime.SetEffort(level)
}

// Thinking returns the current thinking config.
func (m *model) Thinking() *providers.ThinkingConfig {
	if m.runtime == nil {
		return nil
	}
	return m.runtime.CurrentThinking()
}

// SetThinking sets the thinking config for the active model.
func (m *model) SetThinking(cfg providers.ThinkingConfig) error {
	if m.runtime == nil {
		return fmt.Errorf("runtime not available")
	}
	return m.runtime.SetThinking(cfg)
}

// ToggleThinking toggles boolean thinking.
func (m *model) ToggleThinking() (bool, error) {
	if m.runtime == nil {
		return false, fmt.Errorf("runtime not available")
	}
	return m.runtime.ToggleThinking()
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
