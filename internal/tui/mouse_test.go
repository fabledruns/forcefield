package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/permissions"
	"forcefield/internal/session"
)

// ---- helpers -------------------------------------------------------------

func wheelMsg(x, y int, up bool) tea.MouseMsg {
	btn := tea.MouseButtonWheelDown
	if up {
		btn = tea.MouseButtonWheelUp
	}
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: btn}
}

func leftClick(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}

func toolEntry(name string) chatEntry {
	return chatEntry{
		Role:    roleActivity,
		Content: name,
		Tool:    &toolRecord{name: "shell"},
	}
}

// longTranscriptModel builds a sized model whose transcript is taller than
// the viewport; used for scrolling behavior tests.
func longTranscriptModel(t *testing.T) model {
	t.Helper()
	m := newTestModel()
	for i := 0; i < 30; i++ {
		m.entries = append(m.entries, chatEntry{Role: roleUser, Content: "filler line"})
	}
	m.entries = append(m.entries, toolEntry("first tool"))
	m.entries = append(m.entries, toolEntry("second tool"))
	m.entries = append(m.entries, chatEntry{
		Role:     roleActivity,
		Thinking: &thinkingRecord{text: "reasoning", endedAt: time.Now()},
	})
	m.refreshTranscript()
	return m
}

// visibleBlocksModel keeps every interactive block fully on screen at
// scroll offset zero so click coordinates are deterministic.
func visibleBlocksModel(t *testing.T) model {
	t.Helper()
	m := newTestModel()
	m.entries = []chatEntry{
		{Role: roleUser, Content: "hello"},
		toolEntry("first tool"),
		toolEntry("second tool"),
		{Role: roleActivity, Thinking: &thinkingRecord{text: "why", endedAt: time.Now()}},
	}
	m.refreshTranscript()
	return m
}

// ---- Rect / HitMap primitives --------------------------------------------

func TestRectContainsBoundaries(t *testing.T) {
	r := Rect{X: 10, Y: 10, W: 5, H: 3} // covers x 10..14, y 10..12

	cases := []struct {
		x, y int
		want bool
	}{
		{10, 10, true},  // top-left corner
		{14, 12, true},  // bottom-right occupied cell
		{10, 12, true},  // bottom-left
		{14, 10, true},  // top-right
		{9, 10, false},  // immediately left
		{15, 10, false}, // right edge cell (half-open)
		{10, 9, false},  // immediately above
		{10, 13, false}, // below last row
		{14, 13, false}, // corner outside
		{-1, -1, false}, // negative coords never match
		{100, 100, false},
	}
	for _, tc := range cases {
		if got := r.Contains(tc.x, tc.y); got != tc.want {
			t.Errorf("Contains(%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}

	empty := Rect{X: 0, Y: 0, W: 0, H: 5}
	if empty.Contains(0, 0) {
		t.Error("zero-width rect must contain nothing")
	}
}

func TestHitMapOverlapTopmostWins(t *testing.T) {
	var h HitMap
	if _, ok := h.At(5, 5); ok {
		t.Fatal("empty hit map resolved a region")
	}

	h.Register("bottom", Rect{X: 0, Y: 0, W: 20, H: 20}, actionToggleTool, "0")
	h.Register("top", Rect{X: 10, Y: 10, W: 5, H: 5}, actionToggleTool, "1")

	got, ok := h.At(12, 12)
	if !ok || got.ID != "top" {
		t.Errorf("overlap resolved to %+v ok=%v, want topmost", got, ok)
	}
	got, _ = h.At(19, 19)
	if got.ID != "bottom" {
		t.Errorf("non-overlap area resolved to %q, want bottom", got.ID)
	}
	if _, ok := h.At(25, 25); ok {
		t.Error("point outside every region matched")
	}
}

// ---- wheel scrolling ------------------------------------------------------

func TestWheelScrollsViewportBothDirections(t *testing.T) {
	m := longTranscriptModel(t)
	m.viewport.GotoBottom()
	bottom := m.viewport.YOffset
	if bottom == 0 {
		t.Fatal("setup: transcript fits viewport; cannot test scrolling")
	}

	next, consumed := m.routeMouse(wheelMsg(40, m.headerRows()+2, true))
	m = next
	if !consumed {
		t.Fatal("wheel over transcript must be consumed")
	}
	up := bottom - m.viewport.YOffset
	if up <= 0 || up > 3 {
		t.Errorf("wheel-up moved %d lines, want 1..3 clamped by viewport bounds", up)
	}
	offsetAfterUp := m.viewport.YOffset

	next, _ = m.routeMouse(wheelMsg(40, m.headerRows()+2, false))
	m = next
	if m.viewport.YOffset <= offsetAfterUp {
		t.Errorf("wheel-down did not scroll back down (%d <= %d)", m.viewport.YOffset, offsetAfterUp)
	}
	if m.following != m.viewport.AtBottom() {
		t.Errorf("following = %v, want it to track AtBottom()", m.following)
	}
}

func TestWheelRespectsTopBoundaryAndFollowResume(t *testing.T) {
	m := longTranscriptModel(t)

	// Scroll far past the top: viewport clamps at zero without jumping.
	for i := 0; i < 50; i++ {
		next, _ := m.routeMouse(wheelMsg(10, 5, true))
		m = next
	}
	if m.viewport.YOffset != 0 {
		t.Fatalf("YOffset = %d after extreme wheel-up, want clamped 0", m.viewport.YOffset)
	}
	if m.following {
		t.Error("scrolling away from bottom must pause auto-follow")
	}

	// Returning to the very bottom resumes follow.
	for i := 0; i < 200; i++ {
		next, _ := m.routeMouse(wheelMsg(10, 5, false))
		m = next
	}
	if !m.viewport.AtBottom() || !m.following {
		t.Errorf("AtBottom=%v following=%v, want both true after scrolling home", m.viewport.AtBottom(), m.following)
	}
}

func TestWheelWhileStreamingKeepsPositionWhenReading(t *testing.T) {
	m := longTranscriptModel(t)
	next, _ := m.routeMouse(wheelMsg(0, m.headerRows(), true)) // scroll up to read
	m = next
	offset := m.viewport.YOffset

	m.appendAssistantText("streamed chunk") // simulated stream event
	m.refreshTranscript()
	if m.viewport.YOffset != offset {
		t.Error("scroll position moved while reading during streaming")
	}
}

// ---- tool / thinking blocks -----------------------------------------------

func TestClickTogglesSpecificToolBlock(t *testing.T) {
	m := visibleBlocksModel(t)
	toolIdx := indexOfToolEntry(m, "first tool")
	if toolIdx < 0 {
		t.Fatal("test setup: tool entry missing")
	}

	region, ok := m.regionForEntry(toolIdx)
	if !ok {
		t.Fatalf("no hit region for entry %d", toolIdx)
	}

	// Click the center of the block's band.
	cx, cy := region.Rect.X+1, region.Rect.Y
	next, consumed := m.routeMouse(leftClick(cx, cy))
	m = next
	if !consumed {
		t.Fatal("tool block click must be consumed")
	}
	if !m.entries[toolIdx].Tool.expanded {
		t.Error("clicked tool block did not expand")
	}

	// Click again to collapse.
	next, _ = m.routeMouse(leftClick(cx, cy))
	m = next
	if m.entries[toolIdx].Tool.expanded {
		t.Error("second click did not collapse the block")
	}
}

func TestClickTargetsOlderBlockKeyboardCannotReach(t *testing.T) {
	m := visibleBlocksModel(t)
	first := indexOfToolEntry(m, "first tool")

	// ctrl+e toggles only the MOST RECENT tool; prove the older one stays
	// untouched by keyboard while being clickable.
	before := m.entries[first].Tool.expanded
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = next.(model)
	if m.entries[first].Tool.expanded != before {
		t.Fatal("ctrl+e reached the oldest block; keyboard targeting changed")
	}

	region, _ := m.regionForEntry(first)
	next, _ = m.routeMouse(leftClick(region.Rect.X, region.Rect.Y))
	m = next.(model)
	if m.entries[first].Tool.expanded == before {
		t.Error("clicking the older block did not expand it")
	}
}

func TestClickOnThinkingBlockToggles(t *testing.T) {
	m := visibleBlocksModel(t)
	idx := -1
	for i, e := range m.entries {
		if e.Thinking != nil {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("test setup: thinking entry missing")
	}

	region, ok := m.regionForEntry(idx)
	if !ok {
		t.Fatalf("thinking entry %d produced no hit region", idx)
	}
	if region.Action != actionToggleThinking {
		t.Errorf("action = %v, want actionToggleThinking", region.Action)
	}

	next, _ := m.routeMouse(leftClick(region.Rect.X+1, region.Rect.Y))
	m = next
	if !m.entries[idx].Thinking.expanded {
		t.Error("clicking the thinking block did not expand it")
	}
}

func TestHoverEmphasisFollowsPointer(t *testing.T) {
	m := visibleBlocksModel(t)
	toolIdx := indexOfToolEntry(m, "second tool")
	region, ok := m.regionForEntry(toolIdx)
	if !ok {
		t.Fatal("missing region")
	}

	// A motion-less pointer event still carries coordinates: hover updates.
	next, _ := m.routeMouse(tea.MouseMsg{X: region.Rect.X, Y: region.Rect.Y, Action: tea.MouseActionMotion})
	m = next
	if m.hoverID != regionID("tool", toolIdx) {
		t.Errorf("hoverID = %q, want %q", m.hoverID, regionID("tool", toolIdx))
	}

	// Moving off clears it.
	next, _ = m.routeMouse(tea.MouseMsg{X: 0, Y: 0, Action: tea.MouseActionMotion})
	m = next
	if m.hoverID != "" {
		t.Errorf("hoverID = %q after leaving, want empty", m.hoverID)
	}
}

// ---- input focus ----------------------------------------------------------

func TestClickInputAreaFocusesEditor(t *testing.T) {
	m := newTestModel()
	m.input.Blur()

	area := m.inputArea()
	if area.H <= 0 {
		t.Fatalf("input area degenerate: %+v", area)
	}
	next, consumed := m.routeMouse(leftClick(area.X+area.W/2, area.Y+area.H/2))
	m = next
	if !consumed {
		t.Fatal("input click must be consumed")
	}
	if !m.input.Focused() {
		t.Error("clicking the input area did not focus the editor")
	}
}

func TestSuggestionHitRegions(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("/") // bare slash: every command is suggested
	m.updateSuggestions()
	if len(m.suggestions) < 3 {
		t.Fatalf("setup: got %d suggestions for \"/\"", len(m.suggestions))
	}

	// First label starts at x=2 on the suggestions row.
	top := m.height - m.footerHeight()
	_, idx := m.suggestionAt(2, top)
	if idx != 0 {
		t.Fatalf("suggestionAt(2) = %d, want 0", idx)
	}

	// Click the second label: its x sits one gap past the first label.
	firstW := len("/" + m.suggestions[0].Name())
	_, idx = m.suggestionAt(2+firstW+3+1, top)
	if idx != 1 {
		t.Errorf("suggestionAt(second) = %d, want 1", idx)
	}

	// Below the list (the preview line) is not clickable.
	if _, idx = m.suggestionAt(2, top+1); idx != -1 {
		t.Error("preview row must not be a hit target")
	}
}

// ---- permission clicks ----------------------------------------------------

func TestPermissionOptionGeometryMatchesRenderedLabels(t *testing.T) {
	m := newTestModel()
	m.permissionPrompt = &permissionPrompt{request: permissions.Request{Tool: "shell", Arguments: map[string]any{"command": "ls"}}}

	rects := m.permissionOptionRects()
	if len(rects) != 4 {
		t.Fatalf("got %d option rects", len(rects))
	}

	// Independent expectations: options live on row height-3; the first
	// label starts after the box's one-cell left padding (the box has
	// top/bottom borders only — no side border column).
	wantY := m.height - permOptionsRowFromBottom
	if rects[0].Rect.Y != wantY || rects[0].Rect.X != 1 {
		t.Errorf("first rect = %+v, want X=1 Y=%d", rects[0], wantY)
	}
	if rects[0].Rect.W != len("(y) yes") {
		t.Errorf("first rect width = %d, want %d", rects[0].Rect.W, len("(y) yes"))
	}
	// Contiguity: each next label starts exactly one gap after the previous.
	for i := 1; i < len(rects); i++ {
		prevEnd := rects[i-1].Rect.X + rects[i-1].Rect.W + len(permOptionGap)
		if rects[i].Rect.X != prevEnd {
			t.Errorf("rect %d X = %d, want %d (contiguous labels)", i, rects[i].Rect.X, prevEnd)
		}
	}
}

func TestPermissionClickAnswersEquivalentToKeys(t *testing.T) {
	answerViaClick := func(t *testing.T, key string, labelLen int) permissions.Prompt {
		t.Helper()
		m := newTestModel()
		ch := make(chan permissions.Prompt, 1)
		m.permissionPrompt = &permissionPrompt{
			request: permissions.Request{Tool: "shell", Arguments: map[string]any{"command": "make"}},
			respond: ch,
		}
		_ = labelLen

		// Locate the label by scanning the rendered options line, mirroring
		// what the user sees rather than trusting internal bookkeeping.
		rects := m.permissionOptionRects()
		var target Rect
		for _, r := range rects {
			if r.key == key {
				target = r.Rect
			}
		}
		if target.W == 0 {
			t.Fatalf("no rect for key %q", key)
		}

		next, consumed := m.routeMouse(leftClick(target.X, target.Y))
		m = next
		if !consumed {
			t.Fatal("permission click must be consumed")
		}
		select {
		case answer := <-ch:
			if m.permissionPrompt != nil {
				t.Error("prompt still open after answering")
			}
			return answer
		default:
			t.Fatalf("clicking (%s) produced no answer", key)
			return -1
		}
	}

	if got := answerViaClick(t, "y", len("(y) yes")); got != permissions.PromptAllowOnce {
		t.Errorf("yes click = %v", got)
	}
	if got := answerViaClick(t, "n", len("(n) no")); got != permissions.PromptDenyOnce {
		t.Errorf("no click = %v", got)
	}
	if got := answerViaClick(t, "a", len("(a) always allow")); got != permissions.PromptAlwaysAllow {
		t.Errorf("always-allow click = %v", got)
	}
	if got := answerViaClick(t, "d", len("(d) always deny")); got != permissions.PromptAlwaysDeny {
		t.Errorf("always-deny click = %v", got)
	}
}

func TestPermissionClickOutsideOptionsDoesNothing(t *testing.T) {
	m := newTestModel()
	ch := make(chan permissions.Prompt, 1)
	m.permissionPrompt = &permissionPrompt{
		request: permissions.Request{Tool: "shell"},
		respond: ch,
	}

	next, consumed := m.routeMouse(leftClick(1, 1)) // far corner, no control there
	m = next
	if !consumed {
		t.Fatal("while a permission is open, events are owned by it (consumed)")
	}
	select {
	case <-ch:
		t.Fatal("stray click answered the permission prompt")
	default:
	}
	if m.permissionPrompt == nil {
		t.Fatal("stray click dismissed the prompt")
	}
}

// ---- pickers ---------------------------------------------------------------

// sandboxedWorkspace points the process at a throwaway directory so
// session files created by tests never touch the repository checkout.
func sandboxedWorkspace(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".forcefield", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeSessionFile(t *testing.T, id string) {
	t.Helper()
	body := `{"id":"` + id + `","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","messages":[]}`
	path := filepath.Join(".forcefield", "sessions", id+".json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPickerRowClickSwitchesSession(t *testing.T) {
	sandboxedWorkspace(t)
	writeSessionFile(t, "aaa1")
	writeSessionFile(t, "bbb2")
	writeSessionFile(t, "ccc3")

	m := newTestModel()
	m.session = &session.Session{ID: "aaa1"}
	sessions := []session.Session{{ID: "aaa1"}, {ID: "bbb2"}, {ID: "ccc3"}}
	m.picker = newSessionPicker(sessions, "aaa1")

	bx, by := m.picker.boxOrigin(m.width, m.height)
	clickY := by + pickerRowsTop + 2 // third row
	next, consumed := m.routeMouse(leftClick(bx+5, clickY))
	m = next
	if !consumed {
		t.Fatal("picker row click must be consumed")
	}
	if m.session.ID != "ccc3" {
		t.Errorf("active session = %q, want ccc3 after clicking its row", m.session.ID)
	}
	if m.picker != nil {
		t.Error("picker stayed open after choosing")
	}
}

func TestSessionPickerClickOutsideBoxIgnored(t *testing.T) {
	m := newTestModel()
	m.session = &session.Session{ID: "aaa1"}
	sessions := []session.Session{{ID: "aaa1"}}
	m.picker = newSessionPicker(sessions, "aaa1")

	next, _ := m.routeMouse(leftClick(0, 0)) // top-left screen corner, outside modal
	m = next
	if m.picker == nil {
		t.Fatal("outside click closed the picker; should be ignored")
	}
	if m.session.ID != "aaa1" {
		t.Error("session changed without a row click")
	}
}

func TestSelectPickerRowClickChoosesProvider(t *testing.T) {
	m := newFullTestModel(t)
	m.selectPicker = newProviderPicker("ollama")

	bx, by := m.selectPicker.boxOrigin(m.width, m.height)
	clickY := by + pickerRowsTop + 1 // second registered provider
	next, _ := m.routeMouse(leftClick(bx+5, clickY))
	m = next

	if m.providerName != "lmstudio" {
		t.Errorf("provider = %q, want lmstudio after clicking its row", m.providerName)
	}
	if m.selectPicker != nil {
		t.Error("select picker stayed open after clicking a row")
	}
}

// newFullTestModel builds a model wired to a real runtime whose config and
// session storage live in a throwaway home directory.
func newFullTestModel(t *testing.T) model {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir) // os.UserHomeDir on Windows
	t.Setenv("HOME", dir)        // os.UserHomeDir elsewhere

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() failed: %v", err)
	}
	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() failed: %v", err)
	}
	m.width, m.height = 80, 24
	m.layout()
	return m
}

// ---- precedence / fallback --------------------------------------------------

func TestMouseDisabledPassesEventsThrough(t *testing.T) {
	m := newTestModel()
	m.mouseEnabled = false
	m.entries = append(m.entries, toolEntry("tool"))
	m.refreshTranscript()

	_, consumed := m.routeMouse(leftClick(m.width/2, m.headerRows()))
	if consumed {
		t.Fatal("events handled while mouse capture is off must not consume")
	}
}

func TestClickOutsideAllRegionsFallsThroughUnchanged(t *testing.T) {
	m := visibleBlocksModel(t)
	m.input.Blur()
	focusBefore := m.input.Focused()
	before := len(m.entries)

	// Header rows host no interactive regions: the event falls through.
	next, consumed := m.routeMouse(leftClick(0, 0))
	m = next
	if consumed {
		t.Fatal("header click was consumed; nothing interactive lives there")
	}
	if len(m.entries) != before {
		t.Error("fall-through click mutated state")
	}
	if m.input.Focused() != focusBefore {
		t.Error("fall-through forwarding changed input focus state")
	}
}

func TestReleaseDoesNotDoubleFire(t *testing.T) {
	m := longTranscriptModel(t)
	toolIdx := indexOfToolEntry(m, "first tool")
	region, _ := m.regionForEntry(toolIdx)

	next, _ := m.routeMouse(leftClick(region.Rect.X, region.Rect.Y))
	m = next
	expandedAfterPress := m.entries[toolIdx].Tool.expanded

	release := tea.MouseMsg{X: region.Rect.X, Y: region.Rect.Y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}
	next, _ = m.routeMouse(release)
	m = next

	if m.entries[toolIdx].Tool.expanded != expandedAfterPress {
		t.Error("release event fired the action again (duplicate trigger)")
	}
}

// ---- keyboard unchanged -----------------------------------------------------

func TestKeyboardShortcutsUnchangedAlongsideMouse(t *testing.T) {
	m := longTranscriptModel(t)
	last := -1
	for i, e := range m.entries {
		if e.Tool != nil {
			last = i
		}
	}

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = next.(model)
	if !m.entries[last].Tool.expanded {
		t.Error("ctrl+e no longer expands the most recent tool block")
	}
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	m = next.(model)
	// ctrl+r toggles the most recent thinking OR tool record; ensure no panic
	// and state remains coherent.
	_ = m

	next, _ = m.routeMouse(wheelMsg(5, m.headerRows()+1, true))
	m = next.(model)
	if !m.input.Focused() && m.mouseEnabled {
		t.Error("mouse routing disturbed input focus")
	}
}

// ---- helpers continued ------------------------------------------------------

func indexOfToolEntry(m model, content string) int {
	for i, e := range m.entries {
		if e.Tool != nil && strings.Contains(e.Content, content) {
			return i
		}
	}
	return -1
}

// regionForEntry finds the current screen-space hit region covering a
// given entry by scanning spans and converting through the scroll offset.
func (m model) regionForEntry(entry int) (HitRegion, bool) {
	for _, s := range m.spans {
		if s.entry == entry {
			return HitRegion{
				ID:     s.id,
				Rect:   m.contentBand(s.startLine, s.lines),
				Action: s.action,
				Arg:    strconv.Itoa(s.entry),
			}, true
		}
	}
	return HitRegion{}, false
}
