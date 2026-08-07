package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/command"
	"forcefield/internal/config"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// minTranscriptHeight is the smallest the scrollable transcript area is
// ever allowed to shrink to, so a very short terminal window still shows
// something usable instead of a zero-height viewport.
const minTranscriptHeight = 3

// headerHeight and footerHeight are the fixed number of terminal rows
// consumed by the header line and the input box + help line,
// respectively. Used to size the transcript viewport on every resize.
const (
	headerHeight = 2
	footerHeight = 3
)

// model is Forcefield's interactive chat state. It is a thin presentation
// layer: it renders a transcript and forwards each submitted message through
// the runtime's streaming agent loop. See the package doc for what it
// deliberately does not add.
type model struct {
	runtime  *runtime.Runtime
	session  *session.Session
	registry *command.Registry
	stream   <-chan runtime.Event

	assistantBuffer string

	agentName    string
	providerName string
	modelName    string

	entries  []chatEntry
	viewport viewport.Model
	input    textinput.Model
	spinner  spinner.Model

	// activeTools maps a running tool call's ID to the index of its live
	// status line in entries, so concurrent tool calls (the scheduler may
	// run several at once) each get their own line that's updated in
	// place as progress events arrive, instead of clobbering a single
	// shared status string.
	activeTools map[string]int

	// picker is non-nil while the /sessions modal is open. It owns
	// nothing beyond its own selection state; switching the active
	// session is still handled by the model.
	picker *sessionPicker

	// selectPicker is non-nil while the /provider or /model modal is
	// open. Like picker, it owns nothing beyond its own selection
	// state.
	selectPicker *selectPicker

	// permissionPrompt is non-nil while a tool's "ask" permission
	// decision is awaiting an answer. See permission.go and asker.go.
	permissionPrompt *permissionPrompt

	width, height int
	waiting       bool // true while a runTask command is in flight
	status        string
	quitting      bool
	ready         bool // true once the first WindowSizeMsg has arrived
}

// newModel builds the initial chat model. cfg is only used to label the
// session header (which agent/provider/model it's talking to); requests use
// the Runtime created below. asker resolves interactive "ask" permission
// decisions via the permission modal instead of the runtime's stdin
// default, which isn't usable once bubbletea has taken over the terminal.
func newModel(cfg *config.Config, sess *session.Session, asker permissions.Asker) model {
	input := textinput.New()
	input.Placeholder = "Ask Forcefield something…"
	input.Prompt = "› "
	input.CharLimit = 4000
	input.Focus()

	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = spinnerStyle

	r, err := runtime.New()
	if err != nil {
		panic(fmt.Sprintf("failed to initialize runtime: %v", err))
	}
	r.SetPermissionAsker(asker)

	entries := sessionEntries(sess)

	return model{
		agentName:    cfg.Agent.Name,
		providerName: cfg.Model.Provider,
		modelName:    cfg.Model.Name,
		input:        input,
		spinner:      spin,
		viewport:     viewport.New(0, 0),
		runtime:      r,
		entries:      entries,
		session:      sess,
		registry:     newRegistry(),
		activeTools:  make(map[string]int),
	}
}

// sessionEntries converts a session's saved messages into the transcript
// entries the viewport renders. Both the initial model construction and
// switching sessions via the picker go through this single function, so
// the conversion logic never has to be kept in sync in two places.
func sessionEntries(sess *session.Session) []chatEntry {
	entries := make([]chatEntry, 0, len(sess.Messages))

	for _, msg := range sess.Messages {
		var entryRole role

		switch msg.Role {
		case "user":
			entryRole = roleUser
		case "assistant":
			entryRole = roleAssistant
		default:
			continue
		}

		entries = append(entries, chatEntry{
			Role:    entryRole,
			Content: msg.Content,
		})
	}

	return entries
}

// Init satisfies tea.Model. There's nothing to load asynchronously at
// startup — config was already loaded before the program started — so
// this only starts the input cursor blinking.
func (m model) Init() tea.Cmd {
	return textinput.Blink
}

// Update satisfies tea.Model.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, nil

	case tea.KeyMsg:
		if m.permissionPrompt != nil {
			if next, handled := m.handlePermissionKey(msg.String()); handled {
				return next, nil
			}
			return m, nil // swallow other keys while the prompt is open
		}
		if m.picker != nil {
			return m.handlePickerKey(msg)
		}
		if m.selectPicker != nil {
			return m.handleSelectPickerKey(msg)
		}
		return m.handleKey(msg)

	case permissionRequestMsg:
		m.permissionPrompt = &permissionPrompt{request: msg.request, respond: msg.respond}
		m.appendActivity(m.permissionPrompt.summary())
		m.refreshTranscript()
		return m, nil

	case streamEventMsg:
		switch msg.Event.Type {
		case runtime.EventText:
			if msg.Event.Text == "" {
				return m, waitForChunk(m.stream)
			}
			m.status = ""
			m.appendAssistantText(msg.Event.Text)
			m.assistantBuffer += msg.Event.Text
		case runtime.EventThinking:
			// Provider thinking content remains available to other runtime
			// consumers. The terminal presents a concise, transient status.
			if msg.Event.Thinking == "" {
				m.status = "Thinking"
			}
		case runtime.EventToolStart:
			m.finishAssistantStream()
			m.startToolActivity(msg.Event.ToolCall)
		case runtime.EventToolProgress:
			m.updateToolActivity(msg.Event.ToolProgress)
		case runtime.EventToolFinish, runtime.EventToolFailed, runtime.EventToolCancelled:
			m.finishToolActivity(msg.Event.ToolResult, msg.Event.Type)
		}

		m.refreshTranscript()
		return m, waitForChunk(m.stream)

	case streamDoneMsg:
		m.waiting = false
		m.stream = nil
		m.status = ""
		m.activeTools = make(map[string]int)
		m.finishAssistantStream()
		if text := strings.TrimSpace(m.assistantBuffer); text != "" {
			m.session.AddMessage("assistant", text)
			_ = m.session.Save()
		}

		m.assistantBuffer = ""
		m.refreshTranscript()
		return m, nil

	case streamErrMsg:
		m.waiting = false
		m.status = ""
		m.activeTools = make(map[string]int)
		m.finishAssistantStream()

		m.entries = append(m.entries, chatEntry{
			Role:    roleError,
			Content: msg.err.Error(),
		})

		m.stream = nil

		m.refreshTranscript()

		return m, nil

	case spinner.TickMsg:
		if !m.waiting {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	// Anything not handled above (mouse events, cursor-blink ticks, etc.)
	// is forwarded to the viewport and input so their own internal
	// animations and scrolling keep working.
	var cmds []tea.Cmd

	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	var inputCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	cmds = append(cmds, inputCmd)

	return m, tea.Batch(cmds...)
}

func (m *model) appendAssistantText(text string) {
	if len(m.entries) == 0 || m.entries[len(m.entries)-1].Role != roleAssistant {
		m.entries = append(m.entries, chatEntry{Role: roleAssistant, Streaming: true})
	}
	m.entries[len(m.entries)-1].Content += text
}

func (m *model) finishAssistantStream() {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Role == roleAssistant && m.entries[i].Streaming {
			m.entries[i].Streaming = false
			return
		}
	}
}

// startToolActivity adds a new live status line for a tool call that just
// started, tracked by ToolCallID so later progress/finish events for this
// same call (which may be interleaved with events from other concurrently
// running calls) update the right line.
func (m *model) startToolActivity(call *providers.ToolCall) {
	if call == nil {
		return
	}
	m.entries = append(m.entries, chatEntry{Role: roleActivity, Content: formatToolStart(call)})
	m.activeTools[call.ID] = len(m.entries) - 1
}

// updateToolActivity refreshes a running tool's status line with its
// latest streamed output (e.g. the most recent line of shell stdout).
func (m *model) updateToolActivity(progress *runtime.ToolProgress) {
	if progress == nil {
		return
	}
	idx, ok := m.activeTools[progress.ToolCallID]
	if !ok || idx >= len(m.entries) {
		return
	}
	m.entries[idx].Content = formatToolProgress(progress)
}

// finishToolActivity replaces a tool's live status line with its final
// outcome and stops tracking it as active.
func (m *model) finishToolActivity(result *runtime.ToolResult, eventType runtime.EventType) {
	if result == nil {
		return
	}
	text := formatToolFinish(result, eventType)
	if idx, ok := m.activeTools[result.ToolCallID]; ok && idx < len(m.entries) {
		m.entries[idx].Content = text
	} else {
		m.appendActivity(text)
	}
	delete(m.activeTools, result.ToolCallID)
}

// activeToolStatus summarizes currently running tool calls for the footer.
// With one tool running it shows that tool's own status line; with several
// it shows a count so the footer stays a single line regardless of how
// many calls the scheduler has in flight.
func (m *model) activeToolStatus() string {
	if len(m.activeTools) == 0 {
		return ""
	}
	if len(m.activeTools) == 1 {
		for id, idx := range m.activeTools {
			_ = id
			if idx < len(m.entries) {
				return m.entries[idx].Content
			}
		}
	}
	return fmt.Sprintf("Running %d tools", len(m.activeTools))
}

func (m *model) appendActivity(text string) {
	if text == "" {
		return
	}
	m.entries = append(m.entries, chatEntry{Role: roleActivity, Content: text})
}

// handleKey processes keyboard input: global shortcuts first, then
// message submission, then falls back to normal text-input editing.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {

	case tea.KeyCtrlC, tea.KeyEsc:
		m.quitting = true
		return m, tea.Quit

	case tea.KeyEnter:
		if m.waiting {
			// A response is already in flight; ignore extra submits
			// instead of queuing or dropping the in-progress request.
			return m, nil
		}
		task := strings.TrimSpace(m.input.Value())
		if task == "" {
			return m, nil
		}
		m.input.Reset()

		if isCommand, err := command.Dispatch(&m, m.registry, task); isCommand {
			if err != nil {
				m.entries = append(m.entries, chatEntry{Role: roleError, Content: err.Error()})
			}
			m.refreshTranscript()
			if m.quitting {
				return m, tea.Quit
			}
			return m, nil
		}

		m.entries = append(m.entries, chatEntry{Role: roleUser, Content: task})
		m.session.AddMessage("user", task)

		if err := m.session.Save(); err != nil {
			m.entries = append(m.entries, chatEntry{
				Role:    roleError,
				Content: fmt.Sprintf("failed to save session: %v", err),
			})
		}

		m.waiting = true
		m.refreshTranscript()

		stream, err := m.runtime.Stream(
			context.Background(),
			m.session.ProviderMessages(),
		)
		if err != nil {
			m.waiting = false
			m.entries = append(m.entries, chatEntry{Role: roleError, Content: fmt.Sprintf("stream failed: %v", err)})
			m.refreshTranscript()
			return m, nil
		}

		m.stream = stream

		return m, tea.Batch(
			m.spinner.Tick,
			waitForChunk(stream),
		)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handlePickerKey processes keys while the /sessions modal is open. It
// never touches the runtime or transcript directly except on Enter,
// where it hands off to switchToSession.
func (m model) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		m.picker.moveUp()
		return m, nil

	case tea.KeyDown:
		m.picker.moveDown()
		return m, nil

	case tea.KeyEsc:
		m.picker = nil
		return m, nil

	case tea.KeyEnter:
		selected := m.picker.selected()
		m.picker = nil
		return m.switchToSession(selected.ID)
	}

	switch msg.String() {
	case "k":
		m.picker.moveUp()
	case "j":
		m.picker.moveDown()
	case "q":
		m.picker = nil
	}
	return m, nil
}

// handleSelectPickerKey processes keys while the /provider or /model
// modal is open. On Enter it hands off to chooseProvider or
// chooseModel depending on which one is open.
func (m model) handleSelectPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		m.selectPicker.moveUp()
		return m, nil

	case tea.KeyDown:
		m.selectPicker.moveDown()
		return m, nil

	case tea.KeyEsc:
		m.selectPicker = nil
		return m, nil

	case tea.KeyEnter:
		opt := m.selectPicker.selected()
		scope := m.selectPicker.scope
		m.selectPicker = nil
		if scope == scopeProvider {
			return m.chooseProvider(opt.ID)
		}
		return m.chooseModel(opt.ID)
	}

	switch msg.String() {
	case "k":
		m.selectPicker.moveUp()
	case "j":
		m.selectPicker.moveDown()
	case "q":
		m.selectPicker = nil
	}
	return m, nil
}

// switchToSession loads the session with id from disk and replaces the
// active in-memory session, transcript, and viewport in place. It never
// spawns a new runtime or restarts the program: the same runtime keeps
// running, just pointed at different conversation history from here on.
func (m model) switchToSession(id string) (tea.Model, tea.Cmd) {
	if m.session != nil && id == m.session.ID {
		return m, nil // already the active session; nothing to do
	}

	sess, err := session.Load(id)
	if err != nil {
		m.entries = append(m.entries, chatEntry{
			Role:    roleError,
			Content: fmt.Sprintf("failed to load session: %v", err),
		})
		m.refreshTranscript()
		return m, nil
	}

	// A stream from the previous session is no longer relevant once we've
	// switched conversations; drop it rather than let it keep appending
	// to the new transcript.
	m.stream = nil
	m.waiting = false
	m.status = ""
	m.assistantBuffer = ""
	m.activeTools = make(map[string]int)

	m.session = sess
	m.entries = sessionEntries(sess)
	m.refreshTranscript()

	return m, nil
}

// layout recomputes the viewport and input widths/heights after a resize.
func (m *model) layout() {
	transcriptHeight := m.height - headerHeight - footerHeight
	if transcriptHeight < minTranscriptHeight {
		transcriptHeight = minTranscriptHeight
	}

	m.viewport.Width = m.width
	m.viewport.Height = transcriptHeight

	// Account for the input box's rounded border + horizontal padding.
	inputWidth := m.width - 4
	if inputWidth < 1 {
		inputWidth = 1
	}
	m.input.Width = inputWidth

	m.refreshTranscript()
}

// refreshTranscript re-renders all entries into the viewport. Once there's
// a conversation, it scrolls to the bottom so the newest message is
// visible; while empty (showing the FORCEFIELD splash), it stays at the
// top so the whole banner is visible instead of its lower half.
func (m *model) refreshTranscript() {
	if m.viewport.Width == 0 {
		return // not sized yet; layout() will call this again once it is
	}
	m.viewport.SetContent(renderTranscript(m.entries, m.viewport.Width))
	if len(m.entries) == 0 {
		m.viewport.GotoTop()
	} else {
		m.viewport.GotoBottom()
	}
}

// View satisfies tea.Model.
func (m model) View() string {
	if m.quitting {
		return ""
	}
	if !m.ready {
		return "Starting Forcefield…\n"
	}

	if m.picker != nil {
		return m.picker.view(m.width, m.height)
	}
	if m.selectPicker != nil {
		return m.selectPicker.view(m.width, m.height)
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderHeader(),
		m.viewport.View(),
		m.renderFooter(),
	)
}

func (m model) renderHeader() string {
	title := headerStyle.Render(" Forcefield ")
	meta := headerMetaStyle.Render(
		fmt.Sprintf("agent: %s  ·  %s/%s", m.agentName, m.providerName, m.modelName),
	)
	return lipgloss.JoinHorizontal(lipgloss.Top, title, " ", meta)
}

func (m model) renderFooter() string {
	if m.permissionPrompt != nil {
		promptBox := inputBorderStyle.Width(m.width - 2).Render(m.permissionPrompt.footerPrompt())
		return lipgloss.JoinVertical(lipgloss.Left, promptBox, helpStyle.Render("waiting for your answer"))
	}

	inputBox := inputBorderStyle.Width(m.width - 2).Render(m.input.View())

	status := "enter send · esc quit"
	if m.waiting {
		activity := m.activeToolStatus()
		if activity == "" {
			activity = m.status
		}
		if activity == "" {
			activity = "Working"
		}
		status = fmt.Sprintf("%s %s", m.spinner.View(), activity)
	}

	return lipgloss.JoinVertical(lipgloss.Left, inputBox, helpStyle.Render(status))
}
