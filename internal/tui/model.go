package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
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

// headerRows reports how many terminal rows the rendered header actually
// occupies, so viewport sizing and mouse hit-testing both derive chrome
// geometry from the real rendering instead of a hand-maintained constant.
// (A stale constant here shifts every click target by the difference —
// the header renders one row, but was long assumed to be two.)
//
// There is no equivalent fixed footerHeight: the footer grows with the
// input box, which itself grows with however many lines the prompt holds
// (see minInputHeight/maxInputHeight and (*model).layout), so its height
// is computed dynamically instead.
func (m model) headerRows() int {
	if m.width <= 0 {
		return headerMinRows
	}
	return lipgloss.Height(m.renderHeader())
}

// headerMinRows bounds the header when no width has arrived yet and it
// cannot be measured.
const headerMinRows = 1

// minInputHeight and maxInputHeight bound how many rows the prompt input
// box is allowed to occupy. It grows automatically as the user types or
// pastes multiple lines, and shrinks back down on submit, but is capped
// so a very large paste can't swallow the whole terminal window.
const (
	minInputHeight = 1
	maxInputHeight = 6
)

// cachedBlock holds one transcript entry's last rendered block and the
// fingerprint that produced it, so streaming can reuse stable entries.
type cachedBlock struct {
	rendered  string
	lines     int
	role      role
	content   string
	streaming bool
	hovered   bool
	// thinking, when present
	thinkingText      string
	thinkingExpanded  bool
	thinkingStreaming bool
	// tool, when present
	toolPresent   bool
	toolExpanded  bool
	toolFinished  bool
	toolEventType runtime.EventType
	toolErr       string
	toolContent   string
	toolStdout    string
	toolStderr    string
	toolHasExit   bool
	toolExitCode  int
	toolDuration  time.Duration
	toolArgsKey   string
}

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
	input    textarea.Model
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

	// notify delivers messages into the running program from background
	// work (model discovery). It is wired once Start owns the program;
	// nil outside a live program (most tests), where background work is
	// simply skipped.
	notify func(tea.Msg)

	// permissionPrompt is non-nil while a tool's "ask" permission
	// decision is awaiting an answer. See permission.go and asker.go.
	permissionPrompt *permissionPrompt

	// suggestions holds the slash commands whose name has the currently
	// typed prefix, sorted alphabetically; recomputed on every keystroke
	// by updateSuggestions and cleared once the input isn't an
	// in-progress command name (see completion.go). Empty/nil hides the
	// suggestion list and preview entirely.
	suggestions []command.Command

	// tabMatches and tabIndex track an in-progress Tab-cycle: tabMatches
	// is the sorted set of command names a cycle is working through, and
	// tabIndex is which one the input currently holds. Any key other
	// than Tab clears tabMatches, so the next Tab press starts a fresh
	// cycle from whatever's typed then.
	tabMatches []string
	tabIndex   int

	width, height int
	waiting       bool // true while a runTask command is in flight
	status        string
	quitting      bool
	ready         bool // true once the first WindowSizeMsg has arrived

	// following is true while the viewport should stick to the bottom of
	// the transcript as new output streams in. Scrolling up pauses the
	// auto-follow so older output stays put while reading; scrolling back
	// to the bottom (or submitting a message) resumes it.
	following bool

	// streamGen tags the active stream; every waitForChunk message carries
	// the generation it was spawned with, so events from a replaced stream
	// (session switch, /clear, quit) are dropped instead of landing in the
	// new transcript. cancelStream cancels the active stream's context.
	streamGen    uint64
	cancelStream context.CancelFunc

	// showActivity toggles the transient model-activity labels (Thinking,
	// Planning, Running…) in the footer. Footer labels stay one-line
	// status phrases; the reasoning text itself lives in the transcript's
	// collapsible Thinking blocks, not here.
	showActivity bool

	// lastKeyAt timestamps the most recent key event, for paste-burst
	// detection: input drivers without bracketed paste (the Windows
	// console API) deliver a paste as a rapid keystroke burst whose
	// newlines are plain Enter events. A KeyEnter arriving within
	// pasteBurstWindow of the previous key is treated as a pasted newline,
	// not a submit. The window sits well under keyboard auto-repeat
	// (~33ms at the default Windows rate), which never bursts.
	lastKeyAt time.Time

	// mouseEnabled mirrors whether Bubble Tea's mouse tracking is on.
	// It starts enabled (cell motion: clicks, wheel, drag). F2 toggles it
	// so users can hand mouse events back to the terminal for native text
	// selection; keyboard behavior is identical in both modes.
	mouseEnabled bool

	// hoverID is the hit-region ID under the pointer, refreshed from
	// every routed mouse event. Renderers apply subtle emphasis only.
	hoverID string

	// spans maps transcript content rows to interactive entries (tool and
	// thinking blocks), rebuilt whenever the transcript re-renders.
	spans []contentSpan

	// transcript cache for M1/M5: per-entry rendered blocks to avoid
	// full glamour re-parse on every streaming chunk or input keystroke.
	tcacheWidth   int
	tcacheHoverID string
	tcacheBlocks  []cachedBlock
	tcacheContent string
	tcacheSpans   []contentSpan
}

// pasteBurstWindow is the maximum gap between keystrokes for the run to
// count as one paste burst; see model.lastKeyAt.
const pasteBurstWindow = 25 * time.Millisecond

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

// newModel builds the initial chat model. cfg is only used to label the
// session header (which agent/provider/model it's talking to); requests use
// the Runtime created below. asker resolves interactive "ask" permission
// decisions via the permission modal instead of the runtime's stdin
// default, which isn't usable once bubbletea has taken over the terminal.
//
// The error return covers runtime construction (config problems, skill or
// memory loading failures); the caller shows them as a normal startup
// failure instead of crashing.
func newModel(cfg *config.Config, sess *session.Session, asker permissions.Asker) (model, error) {
	return newModelWithConfig(cfg, sess, asker)
}

// newModelWithConfig builds the initial chat model reusing the already-
// loaded Config instead of loading it a second time. This eliminates the
// duplicate config.Load between tui.Start and runtime.New before the first
// frame. runtime.New is kept for the non-TUI ff run path.
func newModelWithConfig(cfg *config.Config, sess *session.Session, asker permissions.Asker) (model, error) {
	input := newInput()

	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = spinnerStyle

	r, err := runtime.NewFromConfig(cfg)
	if err != nil {
		return model{}, fmt.Errorf("initialize runtime: %w", err)
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
		following:    true,
		showActivity: true,
		mouseEnabled: true,
	}, nil
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
	return textarea.Blink
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

	case tea.MouseMsg:
		next, consumed := m.routeMouse(msg)
		if !consumed {
			// Outside every interactive region: keep the pre-mouse
			// forwarding behavior so nothing regresses.
			var vpCmd tea.Cmd
			next.viewport, vpCmd = next.viewport.Update(msg)
			cmds := []tea.Cmd{vpCmd}
			var inputCmd tea.Cmd
			next.input, inputCmd = next.input.Update(msg)
			cmds = append(cmds, inputCmd)
			return next, tea.Batch(cmds...)
		}
		return next, nil

	case permissionRequestMsg:
		// A permission decision blocks the whole run, so it takes
		// precedence over any open modal: close pickers rather than leave
		// the prompt unreachable behind them.
		m.picker = nil
		m.selectPicker = nil
		m.permissionPrompt = &permissionPrompt{request: msg.request, respond: msg.respond, selected: 0}
		m.appendActivity(m.permissionPrompt.summary())
		m.refreshTranscript()
		return m, nil

	case modelsFetchedMsg:
		next := m
		next.applyDiscoveredModels(msg)
		return next, nil

	case streamEventMsg:
		if msg.gen != m.streamGen {
			return m, nil // stale event from a replaced stream
		}
		switch msg.Event.Type {
		case runtime.EventText:
			if msg.Event.Text == "" {
				return m, waitForChunk(m.stream, m.streamGen)
			}
			m.status = ""
			m.finishThinkingStream()
			m.appendAssistantText(msg.Event.Text)
			m.assistantBuffer += msg.Event.Text
		case runtime.EventThinking:
			// Reasoning deltas stream into the transcript's collapsible
			// Thinking block as the model thinks. An empty payload marks
			// the start of a new model turn, closing out the previous
			// turn's block; only reasoning the provider explicitly sent is
			// ever shown.
			if msg.Event.Thinking != "" {
				m.appendThinking(msg.Event.Thinking)
				m.status = "Thinking"
			} else {
				m.finishThinkingStream()
			}
		case runtime.EventToolStart:
			m.finishAssistantStream()
			m.finishThinkingStream()
			m.startToolActivity(msg.Event.ToolCall)
			// Persist the assistant tool_calls batch for /resume replay. The
			// first call of a turn creates a new assistant message; subsequent
			// concurrent calls append to the same batch. Content from the
			// same turn's text stream (assistantBuffer) is attached to the
			// first insertion so provider replay sees identical messages to
			// the in-memory run loop.
			if msg.Event.ToolCall != nil {
				content := strings.TrimSpace(m.assistantBuffer)
				m.session.AppendToolCallToLastAssistant(*msg.Event.ToolCall, content)
				// The buffer now belongs to the persisted assistant turn;
				// start fresh for the next model turn's answer.
				if content != "" {
					m.assistantBuffer = ""
				}
				_ = m.session.Save()
			}
		case runtime.EventToolProgress:
			m.updateToolActivity(msg.Event.ToolProgress)
		case runtime.EventToolFinish, runtime.EventToolFailed, runtime.EventToolCancelled:
			m.finishToolActivity(msg.Event.ToolResult, msg.Event.Type)
			if msg.Event.ToolResult != nil {
				// Persist the tool result so ProviderMessages can replay it.
				content := msg.Event.ToolResult.Content
				// Truncation for provider replay is handled by runtime; store
				// the content as presented to the model (already truncated
				// when produced by scheduler).
				m.session.AddToolResult(msg.Event.ToolResult.ToolCallID, msg.Event.ToolResult.Name, content)
				_ = m.session.Save()
			}
		}

		m.refreshTranscript()
		return m, waitForChunk(m.stream, m.streamGen)

	case streamDoneMsg:
		if msg.gen != m.streamGen {
			return m, nil // stale
		}
		m.stopStream(true)
		m.refreshTranscript()
		return m, nil

	case streamErrMsg:
		if msg.gen != m.streamGen {
			return m, nil // stale
		}
		// Keep whatever streamed before the error: losing a half-finished
		// answer is indistinguishable from the model having said nothing.
		m.stopStream(true)

		m.entries = append(m.entries, chatEntry{
			Role:    roleError,
			Content: msg.err.Error(),
		})

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
	// Wheel/PgUp scrolling away from the bottom pauses auto-follow; coming
	// back to the bottom resumes it.
	m.following = m.viewport.AtBottom()

	var inputCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	cmds = append(cmds, inputCmd)
	m.layout()

	return m, tea.Batch(cmds...)
}

// stopStream tears down the active stream: it cancels the runtime context
// (stopping the producer goroutine and any running tools), retires the
// generation so in-flight reader commands are dropped, and clears the
// per-stream bookkeeping. When savePartial is set, an assistant reply that
// streamed before the stream ended is persisted rather than discarded.
func (m *model) stopStream(savePartial bool) {
	if m.cancelStream != nil {
		m.cancelStream()
		m.cancelStream = nil
	}
	m.streamGen++
	m.stream = nil
	m.waiting = false
	m.status = ""
	m.activeTools = make(map[string]int)
	m.finishAssistantStream()
	m.finishThinkingStream()
	if savePartial {
		if text := strings.TrimSpace(m.assistantBuffer); text != "" {
			m.session.AddMessage("assistant", text)
			_ = m.session.Save()
		}
	}
	m.assistantBuffer = ""
}

func (m *model) appendAssistantText(text string) {
	if len(m.entries) == 0 || m.entries[len(m.entries)-1].Role != roleAssistant {
		m.entries = append(m.entries, chatEntry{Role: roleAssistant, Streaming: true})
	}
	m.entries[len(m.entries)-1].Content += text
}

// appendThinking appends one streamed reasoning chunk to the transcript's
// live Thinking block, creating the block on the turn's first chunk. The
// text never flows into the assistant entry or assistantBuffer, which only
// EventText feeds.
func (m *model) appendThinking(text string) {
	if text == "" {
		return
	}
	if i := m.lastStreamingThinking(); i >= 0 {
		m.entries[i].Thinking.text += text
		return
	}
	m.entries = append(m.entries, chatEntry{
		Role:     roleActivity,
		Thinking: &thinkingRecord{text: text, startedAt: time.Now()},
	})
}

// lastStreamingThinking returns the index of the live Thinking block, or
// -1 when none is streaming. Scanning stops at the most recent thinking
// entry: if it is already closed, later reasoning belongs to a new block.
func (m *model) lastStreamingThinking() int {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Thinking != nil {
			if m.entries[i].Thinking.streaming() {
				return i
			}
			return -1
		}
	}
	return -1
}

// finishThinkingStream freezes the live Thinking block's duration and
// collapses it to its summary line. Called when the turn moves on to
// answer text, tool calls, or completion.
func (m *model) finishThinkingStream() {
	if i := m.lastStreamingThinking(); i >= 0 {
		m.entries[i].Thinking.endedAt = time.Now()
	}
}

// toggleThinkingExpansion flips the expanded view of the most recent
// Thinking block (ctrl+r). Blocks stay collapsed by default once the
// reasoning has finished streaming.
func (m *model) toggleThinkingExpansion() {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Thinking != nil {
			m.entries[i].Thinking.expanded = !m.entries[i].Thinking.expanded
			return
		}
	}
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
	record := &toolRecord{name: call.Name, args: call.Arguments}
	m.entries = append(m.entries, chatEntry{
		Role:    roleActivity,
		Content: formatToolStart(call),
		Tool:    record,
	})
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
		if record := m.entries[idx].Tool; record != nil {
			fillToolRecord(record, result, eventType)
		}
	} else {
		record := &toolRecord{name: result.Name, args: result.Arguments}
		fillToolRecord(record, result, eventType)
		m.appendToolActivity(text, record)
	}
	delete(m.activeTools, result.ToolCallID)
}

// fillToolRecord copies a finished tool call's structured outcome into its
// transcript record for the expandable detail view.
func fillToolRecord(record *toolRecord, result *runtime.ToolResult, eventType runtime.EventType) {
	record.finished = true
	record.eventType = eventType
	record.content = result.Content
	record.stdout = result.Stdout
	record.stderr = result.Stderr
	record.exitCode = result.ExitCode
	record.hasExit = result.HasExitCode
	record.duration = result.Duration
	if result.Err != nil {
		record.err = result.Err.Error()
	}
}

// toggleToolExpansion flips the expanded view of the most recent tool-call
// entry (ctrl+e). Tool calls stay compact by default.
func (m *model) toggleToolExpansion() {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Tool != nil {
			m.entries[i].Tool.expanded = !m.entries[i].Tool.expanded
			return
		}
	}
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

func (m *model) appendToolActivity(text string, record *toolRecord) {
	if text == "" {
		return
	}
	m.entries = append(m.entries, chatEntry{Role: roleActivity, Content: text, Tool: record})
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
		m.tabMatches = nil
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
		// Cancel the in-flight run (stopping tools and the producer
		// goroutine) and persist any partial reply before quitting.
		m.stopStream(true)
		m.quitting = true
		return m, tea.Quit

	case tea.KeyEsc:
		// Esc first clears a non-empty input (or closes suggestions),
		// quitting only when there's nothing else it could mean. Ctrl+C
		// remains the unconditional quit.
		if m.input.Value() != "" {
			m.input.Reset()
			m.suggestions = nil
			m.tabMatches = nil
			m.layout()
			return m, nil
		}
		m.stopStream(true)
		m.quitting = true
		return m, tea.Quit

	case tea.KeyCtrlE:
		m.toggleToolExpansion()
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

	case tea.KeyF2:
		// Toggle mouse capture. Off hands the pointer back to the terminal
		// for native text selection; on restores scrolling/clicks. Keyboard
		// behavior is identical either way.
		m.mouseEnabled = !m.mouseEnabled
		if m.hoverID != "" {
			m.hoverID = ""
			m.refreshTranscript()
		}
		if m.mouseEnabled {
			return m, tea.EnableMouseCellMotion
		}
		return m, tea.DisableMouse

	case tea.KeyEnter:
		// Enter-with-modifier inserts a real newline instead of
		// submitting; see newInput for which chords can actually reach
		// here per platform. An unmodified Enter arriving inside a
		// keystroke burst is a pasted newline (no bracketed paste on the
		// Windows console driver), not a deliberate submit.
		if newlineEnter(msg, inPasteBurst) {
			m.input.InsertString("\n")
			m.tabMatches = nil
			m.updateSuggestions()
			m.layout()
			return m, nil
		}
		if m.waiting {
			// A response is already in flight; ignore extra submits
			// instead of queuing or dropping the in-progress request.
			return m, nil
		}
		started, quit := m.acceptInput()
		if quit {
			return m, tea.Quit
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
			m.refreshTranscript()
			return m, nil
		}

		m.stream = stream
		m.cancelStream = cancel
		m.streamGen++

		return m, tea.Batch(
			m.spinner.Tick,
			waitForChunk(stream, m.streamGen),
		)
	case tea.KeyTab:
		return m.handleTabComplete()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.tabMatches = nil // any non-Tab edit invalidates an in-progress cycle
	m.updateSuggestions()
	// A paste (or any edit) may have changed the input's line count or
	// the suggestion list, either of which changes how tall the footer
	// is, so re-derive the viewport size from the current content.
	m.layout()
	return m, cmd
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
	m.tabMatches = nil
	m.layout() // the input box just shrank back to one line

	if isCommand, err := command.Dispatch(m, m.registry, task); isCommand {
		if err != nil {
			m.entries = append(m.entries, chatEntry{Role: roleError, Content: err.Error()})
		}
		m.refreshTranscript()
		return false, m.quitting
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
	// switched conversations: cancel it (its events would otherwise keep
	// appending to the new transcript) and save any partial reply that
	// belongs to the old session before swapping it out.
	m.stopStream(true)
	m.permissionPrompt = nil

	m.session = sess
	m.entries = sessionEntries(sess)
	m.refreshTranscript()

	return m, nil
}

// syncInputHeight grows or shrinks the input box to match how many lines
// it currently holds (e.g. after a multi-line paste, or after Reset),
// clamped to [minInputHeight, maxInputHeight] so a huge paste can't push
// the transcript off-screen entirely.
func (m *model) syncInputHeight() {
	lines := m.input.LineCount()
	if lines < minInputHeight {
		lines = minInputHeight
	}
	if lines > maxInputHeight {
		lines = maxInputHeight
	}
	if m.input.Height() != lines {
		m.input.SetHeight(lines)
	}
}

// footerHeight computes how many terminal rows the footer needs right
// now: the input box (plus its border), the help/status line below it,
// and, when present, the two-line live suggestion list above it. It
// changes as the input grows/shrinks and as suggestions come and go, so
// callers should not cache this value.
func (m *model) footerHeight() int {
	const (
		inputBorder = 2 // top + bottom border of inputBorderStyle
		helpLine    = 1
		suggestions = 2 // suggestion list line + description line
	)
	h := inputBorder + m.input.Height() + helpLine
	if len(m.suggestions) > 0 {
		h += suggestions
	}
	return h
}

// toolArgsKey returns a stable fingerprint of a tool's argument map.
// It is used as part of the per-entry render cache key so expanded tool
// detail changes invalidate the cached block without needing reflection.
func toolArgsKey(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := sortedArgKeys(args)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		if s, ok := args[k].(string); ok {
			b.WriteString(s)
		} else {
			// Fallback for non-string args: fmt is infrequent (only on
			// expanded detail renders) and keeps the cache correct.
			b.WriteString(fmt.Sprint(args[k]))
		}
		b.WriteString(";")
	}
	return b.String()
}

// layout recomputes the viewport and input widths/heights after a resize
// or after an edit that changed the input's line count or the suggestion
// list, both of which change how tall the footer is.
//
// M5: footer geometry changes (input height, suggestions) do not require a
// full transcript re-parse. Only a width change (which affects wrapping)
// needs to invalidate the markdown cache. When the transcript itself has
// changed, the caller will call refreshTranscript explicitly.
func (m *model) layout() {
	// Account for the input box's rounded border + horizontal padding.
	inputWidth := m.width - 4
	if inputWidth < 1 {
		inputWidth = 1
	}
	m.input.SetWidth(inputWidth)
	m.syncInputHeight()

	transcriptHeight := m.height - m.headerRows() - m.footerHeight()
	if transcriptHeight < minTranscriptHeight {
		transcriptHeight = minTranscriptHeight
	}

	oldWidth := m.viewport.Width
	m.viewport.Width = m.width
	m.viewport.Height = transcriptHeight

	if oldWidth != m.viewport.Width {
		// Width affects wrapping/markdown, so the transcript must be
		// re-rendered (per-entry cache will still reuse stable entries,
		// but wrapping has changed).
		m.refreshTranscript()
		return
	}
	// Height-only change: no transcript re-parse. Keep following semantics
	// without rebuilding markdown.
	if m.following && len(m.entries) > 0 {
		m.viewport.GotoBottom()
	} else if len(m.entries) == 0 {
		// Banner stays centered horizontally; height growth does not
		// require a re-render, but keep viewport at top if empty.
		m.viewport.GotoTop()
	}
}

// refreshTranscript re-renders all entries into the viewport and rebuilds
// the interactive-region layout spans. While the user is following the
// conversation (at the bottom, or just submitted), new output keeps the
// newest message visible; once they've scrolled up to read older output,
// the scroll position is preserved instead of being yanked back down on
// every stream chunk. While empty (showing the FORCEFIELD splash), it
// stays at the top so the whole banner is visible.
//
// M1: incremental rendering – only dirty entries (content, streaming,
// hover, expanded, thinking/tool state, or width) re-parse markdown via
// glamour. Stable entries reuse their cached block, preserving ASCII
// diagram handling and hover emphasis. Width change invalidates all.
func (m *model) refreshTranscript() {
	if m.viewport.Width == 0 {
		return // not sized yet; layout() will call this again once it is
	}
	width := m.viewport.Width
	hoverID := m.hoverID

	if len(m.entries) == 0 {
		content := renderBanner(width)
		m.tcacheWidth = width
		m.tcacheHoverID = hoverID
		m.tcacheBlocks = nil
		m.tcacheSpans = nil
		m.tcacheContent = content
		m.spans = nil
		m.viewport.SetContent(content)
		m.viewport.GotoTop()
		return
	}

	widthChanged := width != m.tcacheWidth

	// Fast early-exit: if nothing has changed at all (same entries
	// fingerprint, same width/hover), avoid rebuilding strings.
	// We still need per-entry checks to know this, but we can avoid
	// SetContent/Goto when everything reuses.
	oldBlocks := m.tcacheBlocks
	newBlocks := make([]cachedBlock, len(m.entries))
	renderedBlocks := make([]string, len(m.entries))
	newSpans := make([]contentSpan, 0, len(m.entries))
	anyDirty := widthChanged || len(oldBlocks) != len(m.entries)
	line := 0
	for i, e := range m.entries {
		hovered := false
		var action mouseAction
		switch {
		case e.Tool != nil:
			hovered = hoverID == regionID("tool", i)
			action = actionToggleTool
		case e.Thinking != nil:
			hovered = hoverID == regionID("think", i)
			action = actionToggleThinking
		default:
			action = actionNone
		}

		canReuse := false
		if !widthChanged && i < len(oldBlocks) {
			cb := oldBlocks[i]
			if cb.role == e.Role && cb.content == e.Content && cb.streaming == e.Streaming && cb.hovered == hovered {
				if e.Thinking != nil {
					if cb.thinkingText == e.Thinking.text && cb.thinkingExpanded == e.Thinking.expanded && cb.thinkingStreaming == e.Thinking.streaming() {
						if e.Thinking.streaming() {
							// Live reasoning header shows elapsed duration;
							// re-render each chunk so the header stays fresh.
							canReuse = false
						} else {
							canReuse = true
						}
					}
				} else if e.Tool != nil {
					if cb.toolPresent && cb.toolExpanded == e.Tool.expanded && cb.toolFinished == e.Tool.finished && cb.toolEventType == e.Tool.eventType && cb.toolErr == e.Tool.err && cb.toolContent == e.Tool.content && cb.toolStdout == e.Tool.stdout && cb.toolStderr == e.Tool.stderr && cb.toolHasExit == e.Tool.hasExit && cb.toolExitCode == e.Tool.exitCode && cb.toolDuration == e.Tool.duration && cb.toolArgsKey == toolArgsKey(e.Tool.args) {
						canReuse = true
					}
				} else {
					// Check cached entry wasn't a tool/thinking block.
					if cb.thinkingText == "" && !cb.toolPresent {
						canReuse = true
					}
				}
			}
		}
		if canReuse {
			renderedBlocks[i] = oldBlocks[i].rendered
			newBlocks[i] = oldBlocks[i]
			// lines already cached in newBlocks[i].lines
		} else {
			anyDirty = true
			block := e.render(width, hovered)
			lines := strings.Count(block, "\n") + 1
			cb := cachedBlock{
				rendered:  block,
				lines:     lines,
				role:      e.Role,
				content:   e.Content,
				streaming: e.Streaming,
				hovered:   hovered,
			}
			if e.Thinking != nil {
				cb.thinkingText = e.Thinking.text
				cb.thinkingExpanded = e.Thinking.expanded
				cb.thinkingStreaming = e.Thinking.streaming()
			}
			if e.Tool != nil {
				cb.toolPresent = true
				cb.toolExpanded = e.Tool.expanded
				cb.toolFinished = e.Tool.finished
				cb.toolEventType = e.Tool.eventType
				cb.toolErr = e.Tool.err
				cb.toolContent = e.Tool.content
				cb.toolStdout = e.Tool.stdout
				cb.toolStderr = e.Tool.stderr
				cb.toolHasExit = e.Tool.hasExit
				cb.toolExitCode = e.Tool.exitCode
				cb.toolDuration = e.Tool.duration
				cb.toolArgsKey = toolArgsKey(e.Tool.args)
			}
			newBlocks[i] = cb
			renderedBlocks[i] = block
		}
		lines := newBlocks[i].lines
		if action != actionNone {
			newSpans = append(newSpans, contentSpan{
				id:        regionID(spanKind(action), i),
				entry:     i,
				startLine: line,
				lines:     lines,
				action:    action,
			})
		}
		line += lines + rowsBetweenEntries
	}

	// If nothing dirty and width/hover stable, viewport already shows the
	// correct content and spans are identical – avoid SetContent which
	// would reset scroll.
	if !anyDirty && !widthChanged && m.tcacheContent != "" {
		// Cache hit: keep existing viewport content, but update in-memory
		// cache slices to the newly built (identical) ones so hover
		// tracking stays correct without re-render.
		// Spans are equivalent to cached ones when not dirty, but we
		// already rebuilt them; reuse cached to avoid churn if equal length.
		// Keep the freshly computed newBlocks/newSpans as cache for next
		// call, but don't touch the viewport.
		m.tcacheBlocks = newBlocks
		m.tcacheSpans = newSpans
		m.spans = newSpans
		return
	}

	content := strings.Join(renderedBlocks, "\n\n")
	m.tcacheWidth = width
	m.tcacheHoverID = hoverID
	m.tcacheBlocks = newBlocks
	m.tcacheContent = content
	m.tcacheSpans = newSpans
	m.spans = newSpans
	m.viewport.SetContent(content)
	if len(m.entries) == 0 {
		m.viewport.GotoTop()
	} else if m.following {
		m.viewport.GotoBottom()
	}
}

// transcriptRegionAt resolves a screen point to an interactive transcript
// region (tool/thinking block), converting through the current scroll
// offset. Spans live in content coordinates, so scrolling never
// invalidates them.
func (m model) transcriptRegionAt(x, y int) (HitRegion, bool) {
	top := m.headerRows()
	if y < top || y >= top+m.viewport.Height {
		return HitRegion{}, false
	}
	contentY := y - top + m.viewport.YOffset
	if span := spanAt(m.spans, contentY); span != nil {
		return HitRegion{
			ID:     span.id,
			Rect:   m.contentBand(span.startLine, span.lines),
			Action: span.action,
			Arg:    strconv.Itoa(span.entry),
		}, true
	}
	return HitRegion{}, false
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

// headerState returns the icon and style that summarize the agent's
// current activity at a glance: idle, thinking, or running a tool. It
// deliberately collapses several runtime states into three visual ones so
// the header stays a single glance, not a status dashboard.
func (m model) headerState() (Icon, lipgloss.Style, string) {
	if m.permissionPrompt != nil {
		return IconWarning, statusWarnStyle, "waiting"
	}
	if !m.waiting {
		return IconIdle, statusIdleStyle, "idle"
	}
	if len(m.activeTools) > 0 {
		return IconRunning, statusBusyStyle, "running"
	}
	return IconThink, statusBusyStyle, "thinking"
}

func (m model) renderHeader() string {
	title := headerStyle.Render(" Forcefield ")

	icon, style, label := m.headerState()
	state := style.Render(fmt.Sprintf("%s %s", icon, label))

	reasoningTag := ""
	if m.runtime != nil {
		caps := m.runtime.CurrentReasoningCapabilities()
		if caps.SupportsEffort() {
			if lvl := m.runtime.CurrentEffort(); lvl != "" {
				reasoningTag += fmt.Sprintf(" · effort:%s", lvl)
			}
		}
		if caps.SupportsThinking() {
			if tc := m.runtime.CurrentThinking(); tc != nil {
				switch caps.Thinking.Kind {
				case providers.ThinkingKindBool:
					if tc.Enabled != nil {
						if *tc.Enabled {
							reasoningTag += " · thinking:on"
						} else {
							reasoningTag += " · thinking:off"
						}
					}
				case providers.ThinkingKindBudget:
					if tc.Budget != nil {
						reasoningTag += fmt.Sprintf(" · thinking:%d", *tc.Budget)
					} else if tc.Enabled != nil {
						if *tc.Enabled {
							reasoningTag += " · thinking:on"
						} else {
							reasoningTag += " · thinking:off"
						}
					}
				case providers.ThinkingKindEnum:
					if tc.Level != "" {
						reasoningTag += fmt.Sprintf(" · thinking:%s", tc.Level)
					}
				}
			}
		}
	}

	meta := headerMetaStyle.Render(
		fmt.Sprintf("%s %s %s  %s %s%s", m.providerName, IconModel, m.modelName, IconSession, m.agentName, reasoningTag),
	)
	return lipgloss.JoinHorizontal(lipgloss.Top, title, " ", state, headerSepStyle.Render(" · "), meta)
}

func (m model) renderFooter() string {
	if m.permissionPrompt != nil {
		promptBox := inputBorderStyle.Width(m.width - 2).Render(m.permissionPrompt.footerPrompt(m.permHoverKey()))
		return lipgloss.JoinVertical(lipgloss.Left, promptBox, helpStyle.Render("waiting for your answer · esc means no"))
	}

	inputBox := inputBorderStyle.Width(m.width - 2).Render(m.input.View())

	status := helpStyle.Render("enter send · alt+enter newline · ctrl+e tool · ctrl+r think · ctrl+t status · f2 mouse/selection · esc quit")
	if m.waiting {
		activity := ""
		if m.showActivity {
			activity = m.activeToolStatus()
			if activity == "" {
				activity = m.status
			}
		}
		if activity == "" && m.showActivity {
			activity = "Working"
		}
		if activity != "" {
			status = statusBusyStyle.Render(fmt.Sprintf("%s %s", m.spinner.View(), activity))
		} else {
			status = statusBusyStyle.Render(m.spinner.View())
		}
	}

	if suggestions := m.renderSuggestions(); suggestions != "" {
		return lipgloss.JoinVertical(lipgloss.Left, suggestions, inputBox, status)
	}
	return lipgloss.JoinVertical(lipgloss.Left, inputBox, status)
}
