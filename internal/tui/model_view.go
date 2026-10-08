package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/perfmark"
	"forcefield/internal/providers"
	"forcefield/internal/recovery"
	"forcefield/internal/session"
)

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
		m.setNotice(statusError, fmt.Sprintf("failed to load session: %v", err))
		m.refreshTranscript()
		return m, nil
	}
	// Heal turns interrupted by an earlier quit/crash before adopting:
	// otherwise the first replay of this session would carry dangling
	// tool calls that strict provider APIs reject.
	recovery.Heal(sess)

	// A stream from the previous session is no longer relevant once we've
	// switched conversations: cancel it (its events would otherwise keep
	// appending to the new transcript) and save any partial reply that
	// belongs to the old session before swapping it out.
	m.stopStream(true)
	m.permissionPrompt = nil

	// Switch runtime agent to the new session's agent, if any.
	desired := strings.TrimSpace(sess.Agent)
	if desired != "" && m.runtime != nil {
		if err := m.runtime.SetAgent(desired); err != nil {
			m.entries = append(m.entries, chatEntry{
				Role:    roleSystem,
				Content: fmt.Sprintf("Session %s had unknown agent %q; using %s", sess.ID, desired, m.runtime.CurrentAgent()),
			})
			// Persist fallback so the session file is repaired.
			sess.Agent = m.runtime.CurrentAgent()
			_ = sess.Save()
		}
	} else if desired == "" && sess != nil && m.runtime != nil {
		sess.Agent = m.runtime.CurrentAgent()
		_ = sess.Save()
	}
	if m.runtime != nil {
		m.agentName = m.runtime.AgentDisplayName()
		m.providerName = m.runtime.CurrentProvider()
		m.modelName = m.runtime.CurrentModel()
	} else {
		m.agentName = strings.TrimSpace(sess.Agent)
	}

	m.session = sess
	m.entries = sessionEntries(sess)
	m.clearNotice()
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
// and, when open, one row per visible command palette entry above it. It
// changes as the input grows/shrinks and as the palette filters, so
// callers should not cache this value.
func (m *model) footerHeight() int {
	const (
		inputBorder = 2 // top + bottom border of inputBorderStyle
		helpLine    = 1
	)
	h := inputBorder + m.input.Height() + helpLine
	h += m.suggestionsHeight()
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
		// Banner stays left-aligned horizontally; height growth does not
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
	starts := groupStarts(m.entries)
	line := 0
	for i, e := range m.entries {
		hovered := false
		var action mouseAction
		var grouped []sysBlock
		sysHover := -1
		if e.Role == roleSystem {
			grouped = parseSysGroup(e.Content)
			if grouped != nil {
				sysHover = sysHoverSection(hoverID, i)
				hovered = sysHover >= 0
			}
		}
		if grouped == nil {
			switch {
			case e.Tool != nil:
				hovered = hoverID == regionID("tool", i)
				action = actionToggleTool
			case e.Thinking != nil:
				hovered = hoverID == regionID("think", i)
				action = actionToggleThinking
			case e.Role == roleSystem && systemCollapsible(e.Content):
				hovered = hoverID == regionID("sys", i)
				action = actionToggleSystem
			default:
				action = actionNone
			}
		}

		var secOffsets []int
		canReuse := false
		if !widthChanged && i < len(oldBlocks) {
			cb := oldBlocks[i]
			if cb.role == e.Role && cb.content == e.Content && cb.streaming == e.Streaming && cb.hovered == hovered && cb.turn == e.Turn && cb.groupStart == starts[i] {
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
					// Check cached entry wasn't a tool/thinking block, and
					// that no system toggle flipped since. Section offsets
					// ride along: same width plus same open states means
					// the same rows.
					if cb.thinkingText == "" && !cb.toolPresent && cb.sysExpanded == e.SysExpanded && cb.sysHover == sysHover && sysSectionsEqual(cb.sysSections, e.SysSections) {
						canReuse = true
					}
				}
			}
		}
		if canReuse {
			renderedBlocks[i] = oldBlocks[i].rendered
			newBlocks[i] = oldBlocks[i]
			secOffsets = oldBlocks[i].sysOffsets
			// lines already cached in newBlocks[i].lines
		} else {
			anyDirty = true
			var block string
			if grouped != nil {
				block, secOffsets = e.renderSysGroup(width, grouped, sysHover)
			} else {
				block = e.renderGrouped(width, hovered, starts[i] || e.Role != roleAssistant)
			}
			lines := strings.Count(block, "\n") + 1
			cb := cachedBlock{
				rendered:   block,
				lines:      lines,
				role:       e.Role,
				content:    e.Content,
				streaming:  e.Streaming,
				hovered:    hovered,
				turn:       e.Turn,
				groupStart: starts[i],
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
			cb.sysExpanded = e.SysExpanded
			cb.sysHover = sysHover
			cb.sysSections = append([]bool(nil), e.SysSections...)
			cb.sysOffsets = secOffsets
			newBlocks[i] = cb
			renderedBlocks[i] = block
		}
		lines := newBlocks[i].lines
		for s, off := range secOffsets {
			newSpans = append(newSpans, contentSpan{
				id:        fmt.Sprintf("syssec:%d:%d", i, s),
				entry:     i,
				startLine: line + off,
				lines:     1,
				action:    actionToggleSysSection,
				sec:       s,
			})
		}
		if action != actionNone {
			newSpans = append(newSpans, contentSpan{
				id:        regionID(spanKind(action), i),
				entry:     i,
				startLine: line,
				lines:     lines,
				action:    action,
			})
		}
		// Mirror joinGrouped: no blank separator row between members of
		// one turn group, so hit regions track the drawn rows exactly.
		step := rowsBetweenEntries
		if i+1 < len(m.entries) && !starts[i+1] {
			step = 0
		}
		line += lines + step
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

	content := joinGrouped(renderedBlocks, starts)
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
		arg := strconv.Itoa(span.entry)
		if span.action == actionToggleSysSection {
			arg = fmt.Sprintf("%d:%d", span.entry, span.sec)
		}
		return HitRegion{
			ID:     span.id,
			Rect:   m.contentBand(span.startLine, span.lines),
			Action: span.action,
			Arg:    arg,
		}, true
	}
	return HitRegion{}, false
}

// View satisfies tea.Model.
func (m model) View() string {
	if firstFrameMarked.CompareAndSwap(false, true) {
		perfmark.EventMem("first-frame")
	}
	if viewPhaseHook != nil {
		viewPhaseHook(m.startupPhase)
	}
	if m.quitting {
		return ""
	}
	if !m.ready {
		return "Starting Forcefield…\n"
	}
	// First frame with real dimensions and content: WindowSizeMsg has
	// arrived (m.ready) and this is not the startup placeholder.
	// first-frame above is left untouched for continuity.
	if firstUsefulFrameMarked.CompareAndSwap(false, true) {
		perfmark.EventMem("first-useful-frame")
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
	title := headerStyle.Render(" superprime™ ")

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

	status := helpStyle.Render("enter send · alt+enter newline · ctrl+e tool · ctrl+r think · ctrl+t status · esc quit")
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
		// The loading indicator is a fixed-width row of blocks at the
		// start of the status line (bottom-left). It carries its own red
		// gradient, so only the activity phrase takes statusBusyStyle;
		// wrapping the blocks would flatten their per-block colors.
		if activity != "" {
			status = fmt.Sprintf("%s %s", m.renderLoading(), statusBusyStyle.Render(activity))
		} else {
			status = m.renderLoading()
		}
	}

	// Crush parity: a notification draws over the help/status line as a
	// full-width solid strip. Same row, same height — it replaces the
	// line above instead of adding chrome, so footerHeight is unchanged.
	if m.notice != nil {
		status = renderStatusBar(m.width, m.notice.kind, m.notice.message)
	}

	if suggestions := m.renderSuggestions(); suggestions != "" {
		return lipgloss.JoinVertical(lipgloss.Left, suggestions, inputBox, status)
	}
	return lipgloss.JoinVertical(lipgloss.Left, inputBox, status)
}
