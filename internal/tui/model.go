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
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
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
// layer: it renders a transcript and forwards each submitted message to
// runtime.Run, exactly like `ff run` would for a single message. See the
// package doc for what it deliberately does not add.
type model struct {
	runtime  *runtime.Runtime
	registry *command.Registry
	stream   <-chan providers.StreamEvent

	agentName    string
	providerName string
	modelName    string

	entries  []chatEntry
	viewport viewport.Model
	input    textinput.Model
	spinner  spinner.Model

	width, height int
	waiting       bool // true while a runTask command is in flight
	quitting      bool
	ready         bool // true once the first WindowSizeMsg has arrived
}

// newModel builds the initial chat model. cfg is only used to label the
// session header (which agent/provider/model it's talking to) — the
// actual call still goes through runtime.Run, which loads config itself.
func newModel(cfg *config.Config) model {
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

	return model{
		agentName:    cfg.Agent.Name,
		providerName: cfg.Model.Provider,
		modelName:    cfg.Model.Name,
		input:        input,
		spinner:      spin,
		viewport:     viewport.New(0, 0),
		runtime:      r,
		registry:     newRegistry(),
	}
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
		return m.handleKey(msg)

	case streamChunkMsg:
		if msg.Text == "" {
			return m, waitForChunk(m.stream)
		}

		if len(m.entries) == 0 ||
			m.entries[len(m.entries)-1].Role != roleAssistant {

			m.entries = append(m.entries, chatEntry{
				Role: roleAssistant,
			})
		}

		m.entries[len(m.entries)-1].Content += msg.Text

		m.refreshTranscript()

		return m, waitForChunk(m.stream)

	case streamDoneMsg:
		m.waiting = false
		m.stream = nil
		m.refreshTranscript()
		return m, nil

	case streamErrMsg:
		m.waiting = false

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
		m.waiting = true
		m.refreshTranscript()

		stream, err := m.runtime.Stream(context.Background(), task)
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
	inputBox := inputBorderStyle.Width(m.width - 2).Render(m.input.View())

	status := "enter send · esc quit"
	if m.waiting {
		status = fmt.Sprintf("%s thinking…", m.spinner.View())
	}

	return lipgloss.JoinVertical(lipgloss.Left, inputBox, helpStyle.Render(status))
}
