package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/command"
	"forcefield/internal/recovery"
)

// newInput builds the prompt's multi-line text input with Forcefield's
// settings. Split out from newModel so tests can construct the exact same
// input widget without spinning up a full model (and its Runtime).
func newInput() textarea.Model {
	input := textarea.New()
	input.Placeholder = "Ask Forcefield something…"
	input.Prompt = "› "
	input.ShowLineNumbers = false
	// textinput's old 4000-char limit was sized for single-line prompts;
	// a pasted file or code block needs more room.
	input.CharLimit = 20000
	input.SetHeight(minInputHeight)
	// A plain Enter always submits (handled in handleKey below); only
	// modified Enters and Ctrl+J insert a literal newline for manually
	// composing a multi-line prompt.
	//
	// Platform reality, per the input drivers in bubbletea v1:
	//   - The Windows console API reports VK_RETURN as a plain KeyEnter
	//     with no Shift state (KeyMsg has Alt, but not Shift, for keys),
	//     so a literal Shift+Enter press is indistinguishable from Enter
	//     there. What it CAN report is Alt+Enter, and ANSI terminals
	//     report it as ESC CR - both arrive as KeyEnter with Alt set,
	//     which handleKey turns into a newline.
	//   - Terminals that send a bare line feed (0x0A) for Shift+Enter
	//     (Kitty, WezTerm, iTerm2 and tmux conventions) arrive as
	//     KeyCtrlJ, which this binding receives.
	//   - If a future input stack ever reports a distinct "shift+enter"
	//     key, handleKey handles it by name before the submit path.
	//
	// Pasted text is unaffected by any of this: bracketed-paste content
	// arrives as one KeyRunes message with Paste set (never as
	// KeyEnter/KeyCtrlJ), and the textarea's sanitizer normalizes \r and
	// \r\n to real \n, so multi-line pastes keep actual newlines. On the
	// Windows console driver (no bracketed paste), a paste arrives as a
	// keystroke burst and handleKey's burst detection converts its Enter
	// events to newlines instead of submitting mid-paste.
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))

	// The default bubbles theme renders the active line's typed text in
	// a dim ANSI grey, which reads as greyed-out/disabled next to the
	// rest of Forcefield's UI. Give focused typed text the same bright
	// foreground the transcript uses, and keep the placeholder in the
	// existing muted color so it stays visibly dimmer than real input.
	focusedStyle, blurredStyle := textarea.DefaultStyles()

	focusedStyle.Text = focusedStyle.Text.Foreground(colorText)
	focusedStyle.CursorLine = focusedStyle.CursorLine.
		Foreground(colorText).
		Background(lipgloss.NoColor{})
	focusedStyle.Placeholder = focusedStyle.Placeholder.Foreground(colorMuted)

	blurredStyle.Text = blurredStyle.Text.Foreground(colorText)
	blurredStyle.CursorLine = blurredStyle.CursorLine.
		Foreground(colorText).
		Background(lipgloss.NoColor{})
	blurredStyle.Placeholder = blurredStyle.Placeholder.Foreground(colorMuted)

	input.FocusedStyle = focusedStyle
	input.BlurredStyle = blurredStyle

	input.Focus()
	return input
}

// handleKey processes keyboard input: global shortcuts first, then
// message submission, then falls back to normal text-input editing.
// newlineEnter reports whether an Enter key event should insert a newline
// rather than submit: modified chords always (Alt+Enter is the reliably
// distinguishable one on every input driver; "shift+enter" only exists on
// input stacks that report it), and a plain Enter when it arrives inside a
// paste keystroke burst, where it carries a pasted newline.
func newlineEnter(msg tea.KeyMsg, inPasteBurst bool) bool {
	return msg.Alt || msg.String() == "shift+enter" || inPasteBurst
}

// normalizeNewlines converts CRLF and lone CR to LF. The textarea's rune
// sanitizer maps each of \r and \n to a separate \n, so unnormalized
// Windows clipboard text would get every newline doubled.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Bracketed paste arrives as one KeyRunes message with Paste set.
	// Insert it verbatim (modulo newline normalization) so multi-line
	// clipboard content keeps real newline characters, and so it can never
	// match a key binding. Being atomic, a paste also never arms the
	// paste-burst window below: an Enter following it is always the
	// user's, never a pasted newline.
	if msg.Type == tea.KeyRunes && msg.Paste {
		m.input.InsertString(normalizeNewlines(string(msg.Runes)))
		m.updateSuggestions()
		m.layout()
		return m, nil
	}

	// Track inter-key timing for paste-burst detection (see
	// pasteBurstWindow): on drivers without bracketed paste (the Windows
	// console API), a paste arrives as a rapid keystroke burst whose
	// embedded newlines are plain Enter events.
	inPasteBurst := time.Since(m.lastKeyAt) <= pasteBurstWindow
	m.lastKeyAt = time.Now()

	switch msg.Type {

	case tea.KeyCtrlC:
		// Active runs are handled before modal/input dispatch in Update. At
		// idle, preserve Ctrl+C as the explicit application quit shortcut.
		m.stopStream(true)
		m.quitting = true
		return m, m.shutdownCmd()

	case tea.KeyEsc:
		// Esc first clears a non-empty input (or closes suggestions),
		// quitting only when there's nothing else it could mean. Ctrl+C
		// remains the unconditional quit.
		if m.input.Value() != "" {
			m.input.Reset()
			m.suggestions = nil
			m.suggestionCursor = 0
			m.layout()
			return m, nil
		}
		m.stopStream(true)
		m.quitting = true
		return m, m.shutdownCmd()

	case tea.KeyCtrlE:
		m.toggleExpandable()
		m.refreshTranscript()
		return m, nil

	case tea.KeyCtrlR:
		m.toggleThinkingExpansion()
		m.refreshTranscript()
		return m, nil

	case tea.KeyCtrlT:
		m.showActivity = !m.showActivity
		return m, nil

	case tea.KeyCtrlY:
		return m, copyLastAssistantMessage(m.entries)

	case tea.KeyEnter:
		// Enter-with-modifier inserts a real newline instead of
		// submitting; see newInput for which chords can actually reach
		// here per platform. An unmodified Enter arriving inside a
		// keystroke burst is a pasted newline (no bracketed paste on the
		// Windows console driver), not a deliberate submit.
		if newlineEnter(msg, inPasteBurst) {
			m.input.InsertString("\n")
			m.updateSuggestions()
			m.layout()
			return m, nil
		}
		// The palette owns plain Enter while open: it confirms the
		// highlighted command into the input (a second Enter runs it).
		if m.paletteOpen() {
			m.selectPaletteActive()
			return m, nil
		}
		if m.waiting && !isNewCommand(m.input.Value()) {
			// A response is already in flight; ignore extra submits
			// instead of queuing or dropping the in-progress request. /new is
			// deliberately allowed through so it can safely cancel and replace
			// a live session.
			return m, nil
		}
		// While the runtime is still initializing (or failed), only the
		// quit command goes through; everything else gets a notice
		// instead of a stream that could never start.
		if task := strings.TrimSpace(m.input.Value()); task != "" && m.startupPhase != startupReady && !isQuitCommand(task) {
			m.status = startupBlocked(m.startupPhase, m.startupErr)
			return m, nil
		}
		started, quit := m.acceptInput()
		if quit {
			return m, m.shutdownCmd()
		}
		if !started {
			return m, nil
		}

		streamCtx, cancel := context.WithCancel(context.Background())
		stream, err := m.runtime.Stream(
			streamCtx,
			m.session.ProviderMessages(),
		)
		if err != nil {
			cancel()
			m.waiting = false
			m.entries = append(m.entries, chatEntry{Role: roleError, Content: fmt.Sprintf("stream failed: %v", err)})
			m.setNotice(statusError, fmt.Sprintf("stream failed: %v", err))
			m.refreshTranscript()
			return m, nil
		}

		m.stream = stream
		m.cancelStream = cancel
		m.streamGen++
		m.loadingFrame = 0

		return m, m.streamPumpCmd()
	case tea.KeyTab:
		return m.handleTabComplete()

	case tea.KeyUp:
		// While the palette is open, arrows navigate it instead of the
		// multiline input; otherwise they keep their textarea behavior.
		if m.paletteOpen() {
			m.movePalette(-1)
			return m, nil
		}
	case tea.KeyDown:
		if m.paletteOpen() {
			m.movePalette(1)
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.updateSuggestions()
	// A paste (or any edit) may have changed the input's line count or
	// the suggestion list, either of which changes how tall the footer
	// is, so re-derive the viewport size from the current content.
	m.layout()
	return m, cmd
}

func isNewCommand(value string) bool {
	parsed, ok := command.Parse(value)
	return ok && parsed.Name == "new"
}

// acceptInput consumes the current input as a submitted prompt: it resets
// the input box, records the message (or runs it when it's a slash
// command), and reports whether the caller should start streaming a reply.
// Split from handleKey so the submission pipeline is testable without a
// live runtime; the newline characters in task are stored verbatim.
func (m *model) acceptInput() (startedStream bool, quit bool) {
	task := strings.TrimSpace(m.input.Value())
	if task == "" {
		return false, false
	}
	m.input.Reset()
	m.suggestions = nil
	m.suggestionCursor = 0
	m.layout() // the input box just shrank back to one line
	// A new submission acknowledges the previous strip notification.
	m.clearNotice()

	if isCommand, err := command.Dispatch(m, m.registry, task); isCommand {
		if err != nil {
			m.entries = append(m.entries, chatEntry{Role: roleError, Content: err.Error()})
			m.setNotice(statusError, err.Error())
		}
		m.refreshTranscript()
		return false, m.quitting
	}

	m.entries = append(m.entries, chatEntry{Role: roleUser, Content: task})
	m.session.AddMessage("user", task)
	// A previous turn may have been cancelled after its tool_calls batch
	// was persisted but before results arrived; heal before the new turn
	// is replayed so every call the provider sees has a result.
	recovery.Heal(m.session)

	if err := m.session.Save(); err != nil {
		m.entries = append(m.entries, chatEntry{
			Role:    roleError,
			Content: fmt.Sprintf("failed to save session: %v", err),
		})
		m.setNotice(statusError, fmt.Sprintf("failed to save session: %v", err))
	}

	m.waiting = true
	m.following = true
	m.refreshTranscript()
	return true, false
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
// chooseModel depending on which one is open; the refresh row (and the
// r hotkey) re-run discovery for the model picker instead.
func (m model) handleSelectPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		m.selectPicker.moveUp()
		m.selectPicker.ensureVisible(m.height)
		return m, nil

	case tea.KeyDown:
		m.selectPicker.moveDown()
		m.selectPicker.ensureVisible(m.height)
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
		if opt.ID == refreshOptionID {
			next := m
			next.openModelPickerFor(m.providerName)
			next.triggerModelRefresh()
			return next, nil
		}
		return m.chooseModel(opt.ID)
	}

	switch msg.String() {
	case "k":
		m.selectPicker.moveUp()
		m.selectPicker.ensureVisible(m.height)
	case "j":
		m.selectPicker.moveDown()
		m.selectPicker.ensureVisible(m.height)
	case "q":
		m.selectPicker = nil
	case "r":
		if m.selectPicker.scope == scopeModel {
			next := m
			next.triggerModelRefresh()
			return next, nil
		}
	}
	return m, nil
}

// triggerModelRefresh re-runs discovery for the open model picker,
// bypassing the cache. The current rows stay visible with a fetching
// indicator until the fresh result replaces them; a failure keeps the
// rows and shows a concise status.
func (m *model) triggerModelRefresh() {
	if m.selectPicker == nil || m.selectPicker.scope != scopeModel || m.selectPicker.fetching {
		return
	}
	m.selectPicker.fetching = true
	m.selectPicker.status = ""
	m.startDiscovery(m.selectPicker.provider, true)
}
