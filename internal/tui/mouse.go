package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Centralized mouse layer: rendering registers hit regions; routeMouse
// resolves events in fixed precedence (permission → modal → wheel →
// transcript → footer). Handled events are consumed; only presses act.
// See docs/TUI.md.

// Rect is an axis-aligned screen or content rectangle. X/Y are inclusive
// origins; W/H are extents in cells/rows. Contains uses half-open bounds
// [X, X+W) so a click exactly on the right/bottom edge belongs to the
// element whose cell it occupies, not to empty space past it.
type Rect struct {
	X, Y, W, H int
}

// Contains reports whether the point lies inside the rect.
func (r Rect) Contains(x, y int) bool {
	if r.W <= 0 || r.H <= 0 {
		return false
	}
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// mouseAction identifies what clicking a region does. Regions carry data
// in Arg rather than closures so hit maps stay comparable, loggable, and
// rebuildable without allocation churn during streaming.
type mouseAction int

const (
	actionNone             mouseAction = iota
	actionToggleTool                   // Arg: entry index of a tool block
	actionToggleThinking               // Arg: entry index of a thinking block
	actionToggleSystem                 // Arg: entry index of a collapsible system block
	actionToggleSysSection             // Arg: "entry:section" of a grouped system block
	actionSessionSelect                // Arg: session picker row index
	actionPickerSelect                 // Arg: selectPicker row index
	actionFocusInput
	actionSuggestionPick // Arg: suggestion index
	actionPermAnswer     // Arg: answer key ("y","n","a","d")
)

// HitRegion is one interactive area with its action and payload.
type HitRegion struct {
	ID     string
	Rect   Rect
	Action mouseAction
	Arg    string
}

// HitMap resolves points to regions. Later registrations win on overlap,
// matching paint order: whatever was drawn last sits "on top".
type HitMap struct {
	regions []HitRegion
}

// Register adds a region to the map.
func (h *HitMap) Register(id string, rect Rect, action mouseAction, arg string) {
	h.regions = append(h.regions, HitRegion{ID: id, Rect: rect, Action: action, Arg: arg})
}

// At returns the topmost region containing the point, if any.
func (h *HitMap) At(x, y int) (HitRegion, bool) {
	for i := len(h.regions) - 1; i >= 0; i-- {
		if h.regions[i].Rect.Contains(x, y) {
			return h.regions[i], true
		}
	}
	return HitRegion{}, false
}

// contentBand returns the Rect for rows [start, start+height) of the
// transcript content when drawn at the given scroll offset. Transcript
// bands are stored in CONTENT coordinates inside spans; converting to
// screen coordinates happens here so scrolling never invalidates them.
// The chrome offset comes from headerRows(), i.e. from the actual
// rendered header, not a constant.
func (m model) contentBand(startLine, height int) Rect {
	y := m.headerRows() + startLine - m.viewport.YOffset
	return Rect{X: 0, Y: y, W: m.width, H: height}
}

// routeMouse resolves one mouse event against current UI state.
// It returns the updated model plus whether the event was fully handled
// (consumed). Unconsumed events keep flowing to viewport/input updates so
// behavior outside interactive regions is unchanged from before mouse
// support existed.
func (m model) routeMouse(msg tea.MouseMsg) (model, bool) {
	if !m.mouseEnabled {
		return m, false
	}

	before := m.hoverID
	m.updateHover(msg)
	if before != m.hoverID {
		// Transcript emphasis is baked into the rendered content; footer
		// and picker emphasis render live from state.
		if strings.HasPrefix(before, "tool:") || strings.HasPrefix(m.hoverID, "tool:") ||
			strings.HasPrefix(before, "think:") || strings.HasPrefix(m.hoverID, "think:") ||
			strings.HasPrefix(before, "sys:") || strings.HasPrefix(m.hoverID, "sys:") ||
			strings.HasPrefix(before, "syssec:") || strings.HasPrefix(m.hoverID, "syssec:") {
			m.refreshTranscript()
		}
	}

	// 1. Permission interaction owns everything while open, mirroring how
	// the keyboard handler swallows unrelated keys.
	if m.permissionPrompt != nil {
		m.routePermissionClicks(msg)
		return m, true
	}

	// 2. Modals own their events entirely: no transcript bleed-through.
	if m.picker != nil || m.selectPicker != nil {
		switch {
		case m.picker != nil:
			m.routePickerClicks(msg)
		case m.selectPicker != nil:
			m.routeSelectPickerClicks(msg)
		}
		return m, true
	}

	// 3+. Main UI: only consume what we actually handled. Clicks outside
	// every interactive region stay unconsumed so the pre-mouse forwarding
	// behavior (viewport/input updates) is preserved unchanged.
	return m, m.routeMainUIMouse(msg)
}

// updateHover records which region (if any) the pointer is over. With
// cell-motion tracking this refreshes on every event that carries
// coordinates (clicks, wheels, drags), giving subtle hover feedback
// without enabling noisy all-motion tracking. Hovering never mutates
// state; renderers consult hoverID for subtle emphasis only.
func (m *model) updateHover(msg tea.MouseMsg) {
	switch {
	case m.permissionPrompt != nil:
		if _, opt := m.permissionOptionAt(msg.X, msg.Y); opt != "" {
			m.hoverID = "perm:" + opt
			return
		}
		m.hoverID = ""
	case m.picker != nil || m.selectPicker != nil:
		// Pickers already highlight their cursor row; no extra hover layer.
		m.hoverID = ""
	default:
		top := m.headerRows()
		if msg.Y >= top && msg.Y < top+m.viewport.Height {
			if region, ok := m.transcriptRegionAt(msg.X, msg.Y); ok {
				m.hoverID = region.ID
				return
			}
		}
		m.hoverID = ""
	}
}

// spanAt finds the layout span covering a content-space row.
func spanAt(spans []contentSpan, line int) *contentSpan {
	for i := range spans {
		if line >= spans[i].startLine && line < spans[i].startLine+spans[i].lines {
			return &spans[i]
		}
	}
	return nil
}

// routePermissionClicks answers clicks on the visible option labels.
// Keyboard semantics are untouched: a click performs exactly what its key
// would, and there are still no accidental confirmations - each label is
// a small, explicit target, and clicks elsewhere do nothing.
func (m *model) routePermissionClicks(msg tea.MouseMsg) {
	if !isLeftPress(msg) {
		return
	}
	if _, key := m.permissionOptionAt(msg.X, msg.Y); key != "" {
		if next, handled := m.handlePermissionKey(key); handled {
			*m = next
		}
	}
}

// permissionOptionAt resolves a footer coordinate to an answer key using
// the same geometry the renderer used to draw the options.
func (m model) permissionOptionAt(x, y int) (int, string) {
	rects := m.permissionOptionRects()
	for _, o := range rects {
		if o.Rect.Contains(x, y) {
			return o.index, o.key
		}
	}
	return -1, ""
}

// routePickerClicks handles the session picker: hovering moves the cursor,
// pressing activates the clicked row (same as keyboard Enter).
func (m *model) routePickerClicks(msg tea.MouseMsg) {
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelUp {
		m.picker.moveUp()
		return
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelDown {
		m.picker.moveDown()
		return
	}
	idx, ok := m.picker.rowAt(msg.X, msg.Y, m.width, m.height)
	if !ok || !isLeftPress(msg) {
		return
	}
	m.picker.cursor = clampIndex(idx, len(m.picker.sessions))
	id := m.picker.sessions[m.picker.cursor].ID
	m.picker = nil
	if next, _ := m.switchToSession(id); next != nil {
		*m = next.(model)
	}
}

// routeSelectPickerClicks mirrors routePickerClicks for provider/model.
func (m *model) routeSelectPickerClicks(msg tea.MouseMsg) {
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelUp {
		m.selectPicker.moveUp()
		m.selectPicker.ensureVisible(m.height)
		return
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelDown {
		m.selectPicker.moveDown()
		m.selectPicker.ensureVisible(m.height)
		return
	}
	idx, ok := m.selectPicker.rowAt(msg.X, msg.Y, m.width, m.height)
	if !ok || !isLeftPress(msg) {
		return
	}
	m.selectPicker.cursor = clampIndex(idx, len(m.selectPicker.options))
	opt := m.selectPicker.selected()
	scope := m.selectPicker.scope
	if scope == scopeModel && opt.ID == refreshOptionID {
		// Clicking the refresh row re-runs discovery; the picker stays open.
		next := m
		next.triggerModelRefresh()
		*m = *next
		return
	}
	m.selectPicker = nil
	var next tea.Model
	if scope == scopeProvider {
		next, _ = m.chooseProvider(opt.ID)
	} else {
		next, _ = m.chooseModel(opt.ID)
	}
	if next != nil {
		*m = next.(model)
	}
}

// routeMainUIMouse handles events in the normal (no-modal) view: wheel
// scrolling first, then click targets. It reports whether the event was
// handled; unhandled events fall through to legacy forwarding.
func (m *model) routeMainUIMouse(msg tea.MouseMsg) bool {
	if wheelDirection(msg) != wheelNone && msg.Action == tea.MouseActionPress {
		m.scrollViewport(wheelDirection(msg))
		return true
	}

	if !isLeftPress(msg) {
		return false
	}

	if region, ok := m.transcriptRegionAt(msg.X, msg.Y); ok {
		m.runRegionAction(region)
		return true
	}

	if _, idx := m.suggestionAt(msg.X, msg.Y); idx >= 0 {
		m.pickSuggestion(idx)
		return true
	}

	if m.inputArea().Contains(msg.X, msg.Y) {
		m.input.Focus()
		m.layout()
		return true
	}
	return false
}

// pickSuggestion completes the input with a clicked palette row, exactly
// as confirming it via Enter would.
func (m *model) pickSuggestion(idx int) {
	m.selectPalette(idx)
}

// suggestionAt resolves a footer point to a palette row index. The palette
// stacks one row per command above the input box, so any x within a
// visible row hits it. Returns (-1, -1) when the point is not on a row.
func (m model) suggestionAt(x, y int) (int, int) {
	if !m.paletteOpen() || m.permissionPrompt != nil || m.picker != nil || m.selectPicker != nil {
		return -1, -1
	}
	top := m.height - m.footerHeight()
	band := Rect{X: 0, Y: top, W: m.width, H: m.paletteShown()}
	if !band.Contains(x, y) {
		return -1, -1
	}
	return y, y - top
}

// inputArea returns the screen rect of the prompt's input box, including
// its border rows, so clicking anywhere in or on it focuses the editor.
func (m model) inputArea() Rect {
	top := m.height - m.footerHeight() + m.suggestionsHeight()
	h := m.input.Height() + 2 // rounded top/bottom border
	if top < 0 || top+h > m.height {
		h = max(0, m.height-top)
	}
	return Rect{X: 0, Y: top, W: m.width, H: h}
}

// suggestionsHeight reports how many rows the command palette adds above
// the input box (zero when closed), matching renderSuggestions.
func (m model) suggestionsHeight() int {
	return m.paletteShown()
}

// runRegionAction performs a transcript region's action. Section regions
// carry an "entry:section" payload and route before the plain-index
// guard below, which only understands bare entry indices.
func (m *model) runRegionAction(region HitRegion) {
	if region.Action == actionToggleSysSection {
		entry, sec, ok := parseSectionArg(region.Arg)
		if !ok {
			return
		}
		if entry < 0 || entry >= len(m.entries) {
			return
		}
		blocks := parseSysGroup(m.entries[entry].Content)
		if blocks == nil || sec < 0 || sec >= sysSectionTotal(blocks) {
			return
		}
		m.setSysSection(entry, sec, !sysSectionOpen(m.entries[entry], sec))
		m.refreshTranscript()
		return
	}
	idx := mustParseIndex(region.Arg)
	if idx < 0 || idx >= len(m.entries) {
		return
	}
	switch region.Action {
	case actionToggleTool:
		if m.entries[idx].Tool != nil {
			m.entries[idx].Tool.expanded = !m.entries[idx].Tool.expanded
			m.refreshTranscript()
		}
	case actionToggleThinking:
		if m.entries[idx].Thinking != nil {
			m.entries[idx].Thinking.expanded = !m.entries[idx].Thinking.expanded
			m.refreshTranscript()
		}
	case actionToggleSystem:
		if m.entries[idx].Role == roleSystem && systemCollapsible(m.entries[idx].Content) {
			m.entries[idx].SysExpanded = !m.entries[idx].SysExpanded
			m.refreshTranscript()
		}
	}
}

// scrollViewport scrolls the existing viewport model by whole lines per
// wheel notch. It never bypasses boundaries (viewport clamps internally)
// and keeps auto-follow semantics identical to keyboard scrolling: leaving
// the bottom pauses follow, returning to it resumes.
//
// It deliberately does not call refreshTranscript: scrolling changes only
// the viewport offset while spans live in content coordinates (see
// transcriptRegionAt), so a refresh would redo the full O(N) entry
// fingerprint only to hit the clean early-exit without touching the
// viewport. Wheel bursts would pay that scan per notch for no effect.
func (m *model) scrollViewport(direction int) {
	const wheelLinesPerNotch = 3
	switch direction {
	case wheelUp:
		m.viewport.LineUp(wheelLinesPerNotch)
	case wheelDown:
		m.viewport.LineDown(wheelLinesPerNotch)
	}
	m.following = m.viewport.AtBottom()
}

// wheel direction values.
const (
	wheelNone = 0
	wheelUp   = -1
	wheelDown = 1
)

func wheelDirection(msg tea.MouseMsg) int {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return wheelUp
	case tea.MouseButtonWheelDown:
		return wheelDown
	default:
		return wheelNone
	}
}

// isLeftPress filters to the one event that means "clicked": initial left
// button press. Releases and motion never trigger actions, preventing
// double-fires from press/release pairs.
func isLeftPress(msg tea.MouseMsg) bool {
	return msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft
}

func clampIndex(i, length int) int {
	if length <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= length {
		return length - 1
	}
	return i
}

// parseSectionArg splits an "entry:section" region payload.
func parseSectionArg(s string) (entry, sec int, ok bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	entry, err1 := parseIndex(parts[0])
	sec, err2 := parseIndex(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return entry, sec, true
}

// parseIndex/mustParseIndex: small helpers for region payloads.
func parseIndex(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errEmptyIndex
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errBadIndex
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func mustParseIndex(s string) int {
	n, err := parseIndex(s)
	if err != nil {
		return -1
	}
	return n
}

var (
	errEmptyIndex = errorString("empty index")
	errBadIndex   = errorString("bad index")
)

type errorString string

func (e errorString) Error() string { return string(e) }
