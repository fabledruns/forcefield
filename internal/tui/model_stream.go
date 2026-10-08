package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/providers"
	"forcefield/internal/recovery"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// stopStream tears down the active stream: it cancels the runtime context
// (stopping the producer goroutine and any running tools), retires the
// generation so in-flight reader commands are dropped, and clears the
// per-stream bookkeeping. When savePartial is set, an assistant reply that
// streamed before the stream ended is persisted rather than discarded.
//
// Teardown-initiated cancellation marks the active turn cancelled (a
// no-op when the turn already ended normally), then repair synthesizes
// results for calls whose terminal events arrive after the generation
// was retired (and are therefore dropped), so the session stays
// replayable and the user can continue or resume.
func (m *model) stopStream(savePartial bool) {
	if m.cancelStream != nil {
		m.cancelStream()
		m.cancelStream = nil
	}
	m.streamGen++
	m.stream = nil
	m.waiting = false
	m.status = ""
	m.loadingFrame = 0
	m.activeTools = make(map[string]int)
	// A build turn abandoned here (session switch, /clear, agent switch,
	// quit) never reaches its terminal handler, so settle a building
	// plan as partial now; the save is best-effort with the failure
	// surfaced once below via noteSaveError.
	if m.turnKind == turnBuild && m.session != nil && m.session.Plan != nil &&
		m.session.Plan.Status == session.PlanBuilding {
		m.session.Plan.Status = session.PlanPartial
		_ = m.session.Save()
	}
	m.turnKind = turnChat
	m.planBuffer = ""
	m.finishAssistantStream()
	m.finishThinkingStream()
	// Shared record path (internal/recovery): keep a reply that streamed
	// before the stream ended rather than discarding it — unless the
	// caller asked to drop it (e.g. /clear, agent switch).
	if savePartial {
		recovery.PersistAssistantText(m.session, m.assistantBuffer)
	}
	// Shared teardown path: settle the turn and synthesize results for
	// calls whose terminal events arrive after the generation was
	// retired (and are therefore dropped).
	recovery.CancelAndRepair(m.session)
	m.assistantBuffer = ""
	// Steady-state saves are fire-and-forget by design, so a failure
	// here would otherwise stay silent: surface it once per distinct
	// error (see also /status, which reports LastSaveError on demand).
	m.noteSaveError()
}

// shutdownTimeout bounds the background cleanup wait on quit: running
// shell jobs and MCP servers get this long to terminate before the UI
// exits anyway. Runtime.Close itself is bounded per subsystem (fast
// job kills, MCP's own grace plus escalation waits); the timeout here
// is the backstop, not the mechanism.
var shutdownTimeout = 5 * time.Second

// shutdownCmd reaps background work, then quits. stopStream already ran
// synchronously in Update (cancelling the run); only Close — which may
// block on child teardown — moves off the UI thread, and only up to
// shutdownTimeout. Nil-runtime models (most tests, failed startup)
// quit immediately.
//
// What this does not cover, explicitly: dying by OS signal (SIGKILL,
// SIGHUP on terminal close, external kill) bypasses Bubble Tea
// teardown entirely — no userspace quit path can intercept those. Long
// sessions belong under tmux/nohup; see docs/Recovery.md.
func (m *model) shutdownCmd() tea.Cmd {
	rt := m.runtime
	return func() tea.Msg {
		if rt != nil {
			done := make(chan error, 1)
			go func() { done <- rt.Close() }()
			select {
			case <-done:
			case <-time.After(shutdownTimeout):
			}
		}
		return tea.Quit()
	}
}

// noteSaveError appends a transcript warning the first time a session
// save error is observed, so a live run whose history stops reaching
// disk is never silent. Nil-safe: teardown paths also run without an
// adopted session.
func (m *model) noteSaveError() {
	if m.session == nil {
		return
	}
	errText := m.session.LastSaveError
	if errText == "" {
		m.saveErrorNotified = ""
		return
	}
	if errText == m.saveErrorNotified {
		return
	}
	m.saveErrorNotified = errText
	m.entries = append(m.entries, chatEntry{
		Role:    roleError,
		Content: fmt.Sprintf("session save failed — recent history is only in memory and will be lost on quit: %s", errText),
	})
	m.setNotice(statusError, fmt.Sprintf("session save failed: %s", errText))
}

// runActive reports whether the TUI still owns a context, stream, tool, or
// permission prompt for an agent turn. It intentionally uses the runtime
// stream lifecycle as the single source of truth instead of maintaining a
// second agent state machine in the presentation layer.
func (m *model) runActive() bool {
	return m.waiting || m.stream != nil || m.cancelStream != nil || m.permissionPrompt != nil
}

// cancelActiveRun retires the current event generation before starting any
// future work. Runtime serializes runs while the cancelled provider/tool
// chain finishes cleanup, and the generation check prevents its late events
// from touching this transcript or a newly-created session.
func (m *model) cancelActiveRun() {
	if !m.runActive() {
		return
	}
	m.stopStream(true)
	m.permissionPrompt = nil
	m.picker = nil
	m.selectPicker = nil
	m.entries = append(m.entries, chatEntry{Role: roleSystem, Content: "Run cancelled."})
	m.setNotice(statusWarn, "Run cancelled.")
	m.refreshTranscript()
}

func (m *model) appendAssistantText(text string) {
	if len(m.entries) == 0 || m.entries[len(m.entries)-1].Role != roleAssistant {
		m.entries = append(m.entries, chatEntry{Role: roleAssistant, Streaming: true, Turn: m.streamGen})
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
		Turn:     m.streamGen,
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
		Turn:    m.streamGen,
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

// toggleExpandable flips the expanded view of the most recent expandable
// entry (ctrl+e): a tool call, a collapsible system block, or — for
// grouped entries — every section at once (expand-all when any section
// is closed, collapse-all otherwise). Tool calls stay compact by
// default; system blocks follow system.go sizing.
func (m *model) toggleExpandable() {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Tool != nil {
			m.entries[i].Tool.expanded = !m.entries[i].Tool.expanded
			return
		}
		if m.entries[i].Role != roleSystem {
			continue
		}
		if blocks := parseSysGroup(m.entries[i].Content); blocks != nil {
			total := sysSectionTotal(blocks)
			allOpen := true
			for s := 0; s < total; s++ {
				if !sysSectionOpen(m.entries[i], s) {
					allOpen = false
					break
				}
			}
			for s := 0; s < total; s++ {
				m.setSysSection(i, s, !allOpen)
			}
			return
		}
		if systemCollapsible(m.entries[i].Content) {
			m.entries[i].SysExpanded = !m.entries[i].SysExpanded
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
	m.entries = append(m.entries, chatEntry{Role: roleActivity, Turn: m.streamGen, Content: text, Tool: record})
}
