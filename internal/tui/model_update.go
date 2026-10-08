package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/perfmark"
	"forcefield/internal/permissions"
	"forcefield/internal/recovery"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

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
// loaded Config instead of loading it a second time. It constructs the
// runtime synchronously (used by tests and any caller that needs a ready
// model immediately); the interactive path (tui.Start) uses
// newStartingModel and installs the runtime in the background instead.
func newModelWithConfig(cfg *config.Config, sess *session.Session, asker permissions.Asker) (model, error) {
	m := newStartingModel(cfg, sess, asker)
	rt, err := defaultRuntimeBuilder(cfg, sess)
	if err != nil {
		return model{}, fmt.Errorf("initialize runtime: %w", err)
	}
	return m.installRuntime(runtimeReadyMsg{rt: rt}), nil
}

// sessionEntries converts a session's saved messages into the transcript
// entries the viewport renders. Both the initial model construction and
// switching sessions via the picker go through this single function, so
// the conversion logic never has to be kept in sync in two places.
func sessionEntries(sess *session.Session) []chatEntry {
	entries := make([]chatEntry, 0, len(sess.Messages))

	for _, msg := range sess.Messages {
		switch msg.Role {
		case "user":
			entries = append(entries, chatEntry{
				Role:    roleUser,
				Content: msg.Content,
			})
		case "assistant":
			// Assistant messages may carry text, tool calls, or both.
			// Text is rendered as an assistant bubble; tool calls are
			// persisted for provider replay and are rendered via their
			// corresponding tool result entries (role=="tool") below, so we
			// don't duplicate them here. An assistant message with only
			// tool calls and no text produces no visible bubble.
			if msg.Content != "" {
				entries = append(entries, chatEntry{
					Role:    roleAssistant,
					Content: msg.Content,
				})
			}
		case "tool":
			// Tool history was previously dropped, losing audit trail after
			// /resume. Render it as a finished, collapsed activity block
			// so the transcript remains useful for debugging.
			rec := &toolRecord{
				name:     msg.Name,
				content:  msg.Content,
				finished: true,
				// Default to finish; the content itself will indicate failure
				// if the tool errored. This keeps the block collapsed and
				// readable without needing exit code/duration metadata.
				eventType: runtime.EventToolFinish,
			}
			summary := msg.Name + ": " + shortResult(msg.Content)
			if summary == ": " {
				summary = msg.Name
			}
			entries = append(entries, chatEntry{
				Role:    roleActivity,
				Content: summary,
				Tool:    rec,
			})
		case "system":
			// Session compaction persists an observable "[compacted …]"
			// system marker so dropped history is explicit in the file.
			// Surface it as a quiet System entry so resumed transcripts
			// show the gap instead of silently jumping. Any other system
			// content stays dropped, as before.
			if !strings.HasPrefix(msg.Content, "[compacted") {
				continue
			}
			entries = append(entries, chatEntry{
				Role:    roleSystem,
				Content: msg.Content,
			})
		default:
			continue
		}
	}

	return entries
}

// Init satisfies tea.Model. The cursor blink starts immediately; runtime
// construction runs as a second command so the first frame renders while
// initialization continues in the background (see startup.go).
func (m model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.initRuntimeCmd())
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
		// Startup marker: fires when this Update returns, i.e. after
		// the key has been processed into state (async work, if any,
		// may still follow). Registered only for key messages.
		defer perfmark.Event("input-processed")
		// Ctrl+C is run control while anything belonging to an agent turn is
		// active, even if a permission modal currently owns keyboard focus.
		// Once idle it falls through to the existing quit behavior below.
		if msg.Type == tea.KeyCtrlC && m.runActive() {
			m.cancelActiveRun()
			return m, nil
		}
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

	case runtimeReadyMsg:
		return m.installRuntime(msg), nil

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
			if m.turnKind == turnPlan {
				m.planBuffer += msg.Event.Text
			}
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
				// Shared record path (internal/recovery): intent and
				// execution state commit in one Save before tools run.
				recovery.RecordToolStart(m.session, *msg.Event.ToolCall, content)
				// The buffer now belongs to the persisted assistant turn;
				// start fresh for the next model turn's answer.
				if content != "" {
					m.assistantBuffer = ""
				}
			}
		case runtime.EventToolProgress:
			m.updateToolActivity(msg.Event.ToolProgress)
		case runtime.EventToolFinish, runtime.EventToolFailed, runtime.EventToolCancelled, runtime.EventToolDenied:
			m.finishToolActivity(msg.Event.ToolResult, msg.Event.Type)
			if msg.Event.ToolResult != nil {
				// Shared record path (internal/recovery): status and
				// outcome commit in one Save so replay always pairs
				// calls with results.
				recovery.RecordToolResult(m.session, msg.Event.Type, msg.Event.ToolResult)
			}
		}

		m.refreshTranscript()
		return m, waitForChunk(m.stream, m.streamGen)

	case turnStartedMsg:
		if msg.gen != m.streamGen || !m.waiting || m.stream == nil {
			return m, nil // superseded before the pump started
		}
		return m, m.streamPumpCmd()

	case streamDoneMsg:
		if msg.gen != m.streamGen {
			return m, nil // stale
		}
		kind, planBody := m.turnKind, m.planBuffer
		// The run finished normally: close the turn before teardown so
		// the persisted record says complete even if the process dies
		// before the next save.
		recovery.NoteTerminal(m.session, runtime.EventDone, nil)
		m.stopStream(true)
		switch kind {
		case turnPlan:
			m.acceptPlanBody(planBody)
		case turnBuild:
			m.setBuildStatus(session.PlanDone, "Build complete.")
		}
		m.refreshTranscript()
		return m, nil

	case streamErrMsg:
		if msg.gen != m.streamGen {
			return m, nil // stale
		}
		kind := m.turnKind
		// Record how the turn ended: user cancellation stays cancelled,
		// anything else (provider failure, timeout, crash-adjacent
		// errors) is an interruption. Either way the pending calls are
		// terminal and will never be re-executed.
		recovery.NoteTerminal(m.session, runtime.EventError, msg.err)
		// Keep whatever streamed before the error: losing a half-finished
		// answer is indistinguishable from the model having said nothing.
		m.stopStream(true)

		errText := ""
		if msg.err != nil {
			errText = msg.err.Error()
		}
		m.entries = append(m.entries, chatEntry{
			Role:    roleError,
			Content: errText,
		})
		m.setNotice(statusError, errText)
		if kind == turnBuild {
			// The error above already explains the failure; the
			// status transition keeps the partial work explicit.
			m.setBuildStatus(session.PlanPartial, "")
		}

		m.refreshTranscript()

		return m, nil

	case streamCancelledMsg:
		if msg.gen != m.streamGen {
			return m, nil // stale
		}
		kind := m.turnKind
		recovery.NoteTerminal(m.session, runtime.EventCancelled, msg.err)
		m.stopStream(true)
		m.entries = append(m.entries, chatEntry{Role: roleSystem, Content: "Run cancelled."})
		m.setNotice(statusWarn, "Run cancelled.")
		if kind == turnBuild {
			m.setBuildStatus(session.PlanPartial, "Partial build — run /build to continue.")
		}
		m.refreshTranscript()
		return m, nil

	case streamBlockedMsg:
		if msg.gen != m.streamGen {
			return m, nil // stale
		}
		kind, planBody := m.turnKind, m.planBuffer
		// The tool batch finished cleanly; the runtime stopped before asking
		// for another model turn. Record that batch as complete, then show
		// the user-facing safety reason as a system entry.
		recovery.NoteTerminal(m.session, runtime.EventBlocked, msg.err)
		m.stopStream(true)
		reason := "Agent stopped."
		if msg.err != nil && strings.TrimSpace(msg.err.Error()) != "" {
			reason = msg.err.Error()
		}
		m.entries = append(m.entries, chatEntry{Role: roleSystem, Content: reason})
		if kind == turnPlan {
			m.acceptPlanBody(planBody)
		}
		if kind == turnBuild {
			m.setBuildStatus(session.PlanPartial, "")
		}
		// Set after plan helpers so the block reason wins over any
		// success strip they may have set.
		m.setNotice(statusWarn, reason)
		m.refreshTranscript()
		return m, nil

	case loadingTickMsg:
		// The block wave advances only while a run is in flight. Dropping
		// the tick when waiting is false stops the chain cleanly on
		// completion, error, or cancellation, and avoids re-rendering the
		// transcript: only the footer reads loadingFrame.
		if !m.waiting {
			return m, nil
		}
		m.loadingFrame++
		// When degraded to static blocks there is no animation to advance,
		// so don't schedule another tick and keep CPU at zero.
		if !loadingSupportsGradient() {
			return m, nil
		}
		return m, loadingTickCmd()
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
