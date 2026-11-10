package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/orchevents"
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
		// The opening animation is NOT tied to startup anymore: the
		// normal TUI is the default surface, and the opening timeline
		// measures from the orchestration activation transition instead
		// (Phase 5). Resizes only lay out.
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
		prevPanelHeight := m.agentsPanelHeight()
		m.agents.apply(msg.Event)
		// The quadrant canvas reads slot assignments; refresh them from
		// the stream feed too, so both event paths keep the canvas
		// live. Slot state is orch-canvas presentation: nothing reads
		// it when the feature is off.
		if m.orchEnabled {
			m.agents.assignQuadrantSlots()
			// The stream feed terminalizes rows too (delegate results,
			// repairs); the view must hand back when the orch feed's
			// own terminal event was dropped or lags behind.
			m.refreshOrchActive()
		}
		if m.agentsPanelHeight() != prevPanelHeight {
			m.layout()
		}
		// A finished delegate child leaves the panel (terminal rows are
		// not rendered) and collapses into one bounded transcript entry.
		if summaries := m.agents.drainCollapsed(); len(summaries) > 0 {
			for _, summary := range summaries {
				m.Println("agent %s", summary)
			}
			m.refreshTranscript()
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

	case openingTickMsg:
		// One frame of the opening animation (orchestration mode only).
		// The tick chain stops itself exactly when the animation is done
		// or was never started, so no scheduled work survives past the
		// 10.6s completion boundary (PLAN §12.1). Advance takes the
		// tick's explicit timestamp: a large scheduler stall lands in
		// the correct final state in one step, and nothing ever sleeps.
		if !m.orchEnabled || !m.anim.Started() || m.anim.Done() {
			return m, nil
		}
		m.anim.Advance(time.Time(msg))
		if m.anim.Done() {
			return m, nil
		}
		return m, openingTickCmd()

	case orchDeliverMsg:
		if msg.gen != m.orchGen {
			return m, nil // stale batch queued before a session reset
		}
		// Fold through the Phase 3 state machine — the same one the
		// stream feed enriches — so ordering, correlation, and
		// first-terminal-wins stay owned by agentsState. Terminal
		// children collapse into bounded transcript entries exactly as
		// the stream feed does; drainCollapsed hands each summary out
		// exactly once no matter which feed finished the row.
		prevPanelHeight := m.agentsPanelHeight()
		var animCmd tea.Cmd
		for _, ev := range msg.events {
			m.agents.applyOrchEvent(ev)
			// Activation is the AgentSpawned event and nothing else:
			// the runtime emits it only after a delegation was accepted,
			// admitted under the concurrency limits, and the child run
			// is actually about to execute. Configuration, tool
			// availability, rejections, and pre-execution failures
			// never emit it, so none of them can switch the view.
			if m.orchEnabled && !m.orchActive && ev.Type == orchevents.AgentSpawned {
				m.orchActive = true
				animCmd = m.maybeStartOpening()
			}
		}
		m.agents.assignQuadrantSlots()
		if m.agentsPanelHeight() != prevPanelHeight {
			m.layout()
		}
		if summaries := m.agents.drainCollapsed(); len(summaries) > 0 {
			for _, summary := range summaries {
				m.Println("agent %s", summary)
			}
			m.refreshTranscript()
		}
		m.observeOrchDrops(msg.drops)
		// Deactivation happens after the whole batch: a completed child
		// must not hide the view while a sibling in the same batch is
		// still running, and a spawn+completion pair in one batch nets
		// out to the correct end state.
		m.refreshOrchActive()
		return m, animCmd
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

// maybeStartOpening latches the opening animation at the orchestration
// opening transition — the orchestration activation (the first
// AgentSpawned of a session), when the quadrant canvas first renders —
// and schedules the first tick. It returns nil (no command) when the
// feature is off, when the animation already latched (Start ignores
// repeats, and the early Started check keeps later activations from
// stacking a second concurrent tick chain), or when the session-reset
// wordmark latch already dismissed it. The animation runs once per
// orchestration session, never once per child: Start is only reachable
// on the session's first activation because the reset sites zero the
// animation state alongside orchGen.
func (m *model) maybeStartOpening() tea.Cmd {
	if !m.orchEnabled || m.anim.Started() || m.anim.Done() {
		return nil
	}
	m.anim.Start(time.Now())
	if !m.anim.Started() || m.anim.Done() {
		return nil
	}
	return openingTickCmd()
}

// refreshOrchActive retires the orchestration view when no orchestration
// work remains: agentsState.active() reports whether any child row is
// still queued or running, across both event feeds. Called after every
// state-mutating feed application (and after stopStream's cancelOpen),
// so the view hands back exactly when the last child finishes — a
// completed child never hides the view while a sibling runs.
func (m *model) refreshOrchActive() {
	if m.orchActive && !m.agents.active() {
		m.orchActive = false
	}
}

// observeOrchDrops reports new lifecycle-event loss exactly once per
// increase. Droppable sheds stay silent by design: the next progress
// event supersedes them and ChildCompleted's counter snapshot
// reconciles what was hidden. A hard drop, by contrast, means the
// panel's history is incomplete — never presenting that as complete is
// exactly why the counters exist. Counters are cumulative, so this is
// O(1) per delivery with no extra storage or queue.
func (m *model) observeOrchDrops(snap orchDropSnapshot) {
	if snap.hard <= m.orchHardDrops {
		return
	}
	lost := snap.hard - m.orchHardDrops
	m.orchHardDrops = snap.hard
	m.Println("system: %d orchestration lifecycle event(s) were dropped under event-queue pressure; agent panel history may be incomplete.", lost)
	m.refreshTranscript()
}
