package tui

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

func sizedModel() model {
	m := newTestModel()
	m.following = true
	m.showActivity = true
	m.activeTools = make(map[string]int)
	return m
}

func TestRefreshTranscriptPreservesScrollWhileReading(t *testing.T) {
	m := sizedModel()
	for i := 0; i < 100; i++ {
		m.entries = append(m.entries, chatEntry{Role: roleUser, Content: strings.Repeat("line ", 5)})
	}
	m.refreshTranscript()
	m.viewport.GotoBottom()

	// User scrolls up to read older output: auto-follow pauses.
	m.viewport.LineUp(10)
	m.following = m.viewport.AtBottom()
	if m.following {
		t.Fatal("scrolling up should pause auto-follow")
	}
	offset := m.viewport.YOffset

	// A streaming chunk arrives: the scroll position must not move.
	m.appendAssistantText("new streamed text")
	m.refreshTranscript()
	if m.viewport.YOffset != offset {
		t.Errorf("YOffset = %d after refresh, want %d (reading position must be preserved)", m.viewport.YOffset, offset)
	}

	// Scrolling back to the bottom resumes following.
	m.viewport.GotoBottom()
	m.following = m.viewport.AtBottom()
	m.appendAssistantText("more text")
	m.refreshTranscript()
	if !m.viewport.AtBottom() {
		t.Error("following the transcript should keep the newest output visible")
	}
}

func TestStaleStreamEventAfterSessionSwitchIsDropped(t *testing.T) {
	m := sizedModel()
	m.streamGen = 7
	ch := make(chan runtime.Event, 1)
	m.stream = ch

	// Event from a previous generation (e.g. after switching sessions):
	// must be dropped, not appended, and must not re-arm a reader.
	next, cmd := m.Update(streamEventMsg{
		Event: runtime.Event{Type: runtime.EventText, Text: "stale"},
		gen:   6,
	})
	got := next.(model)
	if len(got.entries) != 0 {
		t.Fatalf("stale event appended to transcript: %d entries", len(got.entries))
	}
	if got.assistantBuffer != "" {
		t.Errorf("assistantBuffer = %q, want empty", got.assistantBuffer)
	}
	if cmd != nil {
		t.Error("stale event re-armed a stream reader")
	}
}

func TestStaleStreamDoneAfterSwitchIsDropped(t *testing.T) {
	m := sizedModel()
	m.streamGen = 2
	m.waiting = true
	m.assistantBuffer = "old session partial"

	next, _ := m.Update(streamDoneMsg{gen: 1})
	got := next.(model)
	if !got.waiting {
		t.Error("stale done event cleared the waiting state of the new stream")
	}
	if got.assistantBuffer != "old session partial" {
		t.Errorf("stale done event consumed the new buffer")
	}
}

func TestStreamErrorSavesPartialAssistantReply(t *testing.T) {
	m := sizedModel()
	sess := session.New()
	m.session = sess
	t.Cleanup(func() { os.RemoveAll(".forcefield") })
	m.streamGen = 1
	m.waiting = true
	m.assistantBuffer = "partial answer before the failure"
	m.appendAssistantText("partial answer before the failure")

	next, _ := m.Update(streamErrMsg{err: errors.New("connection reset"), gen: 1})
	got := next.(model)

	if got.assistantBuffer != "" {
		t.Errorf("assistantBuffer = %q, want cleared", got.assistantBuffer)
	}
	found := false
	for _, e := range got.entries {
		if e.Role == roleAssistant && e.Content == "partial answer before the failure" {
			found = true
		}
	}
	if !found {
		t.Error("partial assistant reply was lost on stream error")
	}
	if len(sess.Messages) == 0 || sess.Messages[len(sess.Messages)-1].Content != "partial answer before the failure" {
		t.Errorf("partial reply not persisted to session: %+v", sess.Messages)
	}
	last := got.entries[len(got.entries)-1]
	if last.Role != roleError {
		t.Errorf("last entry role = %v, want roleError", last.Role)
	}
}

func TestEventThinkingSetsStatusLabel(t *testing.T) {
	m := sizedModel()
	m.streamGen = 1
	ch := make(chan runtime.Event, 1)
	m.stream = ch
	m.waiting = true

	next, _ := m.Update(streamEventMsg{
		Event: runtime.Event{Type: runtime.EventThinking, Thinking: "(chain of thought elided)"},
		gen:   1,
	})
	got := next.(model)
	if got.status != "Thinking" {
		t.Errorf("status = %q, want %q", got.status, "Thinking")
	}
}

// TestThinkingChunksAccumulateAcrossEvents makes sure multiple streamed
// reasoning deltas append into the same live Thinking block rather than
// creating a new one per chunk, matching how the NVIDIA provider streams
// reasoning_content one fragment at a time.
func TestThinkingChunksAccumulateAcrossEvents(t *testing.T) {
	m := sizedModel()
	m.streamGen = 1
	m.stream = make(chan runtime.Event, 1)
	m.waiting = true

	chunks := []string{"I need to check the file first", " then compile it", " then run it"}
	var next tea.Model = m
	for _, c := range chunks {
		next, _ = next.(model).Update(streamEventMsg{
			Event: runtime.Event{Type: runtime.EventThinking, Thinking: c},
			gen:   1,
		})
	}
	got := next.(model)

	thinkingEntries := 0
	var text string
	for _, e := range got.entries {
		if e.Thinking != nil {
			thinkingEntries++
			text = e.Thinking.text
		}
	}
	if thinkingEntries != 1 {
		t.Fatalf("got %d Thinking entries, want 1 (chunks should accumulate into a single block)", thinkingEntries)
	}
	want := strings.Join(chunks, "")
	if text != want {
		t.Errorf("Thinking.text = %q, want %q", text, want)
	}
}

// TestReasoningDoesNotLeakIntoAssistantContent verifies that reasoning
// deltas are kept entirely separate from the assistant's answer: the
// transcript's assistant entry and the persisted assistantBuffer must
// never contain streamed reasoning text.
func TestReasoningDoesNotLeakIntoAssistantContent(t *testing.T) {
	m := sizedModel()
	m.streamGen = 1
	m.stream = make(chan runtime.Event, 1)
	m.waiting = true

	var next tea.Model = m
	next, _ = next.(model).Update(streamEventMsg{
		Event: runtime.Event{Type: runtime.EventThinking, Thinking: "the model's private chain of thought"},
		gen:   1,
	})
	next, _ = next.(model).Update(streamEventMsg{
		Event: runtime.Event{Type: runtime.EventText, Text: "Here is the answer."},
		gen:   1,
	})
	got := next.(model)

	if strings.Contains(got.assistantBuffer, "chain of thought") {
		t.Errorf("assistantBuffer leaked reasoning text: %q", got.assistantBuffer)
	}
	for _, e := range got.entries {
		if e.Role == roleAssistant && strings.Contains(e.Content, "chain of thought") {
			t.Errorf("assistant entry leaked reasoning text: %q", e.Content)
		}
	}

	found := false
	for _, e := range got.entries {
		if e.Thinking != nil && e.Thinking.text == "the model's private chain of thought" {
			found = true
		}
	}
	if !found {
		t.Error("reasoning text was not recorded in a Thinking entry")
	}
}

// TestThinkingBlockClosesWhenAssistantTextStarts ensures the live
// reasoning block stops streaming (freezes its duration) once the model
// moves on to answer text, so the Thinking header collapses instead of
// continuing to grow indefinitely.
func TestThinkingBlockClosesWhenAssistantTextStarts(t *testing.T) {
	m := sizedModel()
	m.streamGen = 1
	m.stream = make(chan runtime.Event, 1)
	m.waiting = true

	var next tea.Model = m
	next, _ = next.(model).Update(streamEventMsg{
		Event: runtime.Event{Type: runtime.EventThinking, Thinking: "thinking..."},
		gen:   1,
	})
	next, _ = next.(model).Update(streamEventMsg{
		Event: runtime.Event{Type: runtime.EventText, Text: "answer"},
		gen:   1,
	})
	got := next.(model)

	for _, e := range got.entries {
		if e.Thinking != nil && e.Thinking.streaming() {
			t.Error("Thinking block still marked as streaming after assistant text started")
		}
	}
}

// TestToggleThinkingExpansionFlipsMostRecentBlock covers ctrl+r: it should
// expand a collapsed Thinking block and collapse it back on a second call,
// without touching earlier Thinking blocks from prior turns.
func TestToggleThinkingExpansionFlipsMostRecentBlock(t *testing.T) {
	m := sizedModel()
	m.entries = []chatEntry{
		{Role: roleActivity, Thinking: &thinkingRecord{text: "older turn", startedAt: time.Now(), endedAt: time.Now()}},
		{Role: roleActivity, Thinking: &thinkingRecord{text: "latest turn", startedAt: time.Now(), endedAt: time.Now()}},
	}

	m.toggleThinkingExpansion()
	if !m.entries[1].Thinking.expanded {
		t.Error("most recent Thinking block should be expanded after toggle")
	}
	if m.entries[0].Thinking.expanded {
		t.Error("older Thinking block should be untouched by toggle")
	}

	m.toggleThinkingExpansion()
	if m.entries[1].Thinking.expanded {
		t.Error("most recent Thinking block should collapse back on second toggle")
	}
}

// TestThinkingAbsentWhenProviderSendsNone confirms providers/models that
// never emit reasoning (e.g. Ollama, or an NVIDIA model without a
// reasoning_content/reasoning field) produce no Thinking entries at all -
// the block must not appear, let alone be fabricated.
func TestThinkingAbsentWhenProviderSendsNone(t *testing.T) {
	m := sizedModel()
	m.streamGen = 1
	m.stream = make(chan runtime.Event, 1)
	m.waiting = true

	var next tea.Model = m
	next, _ = next.(model).Update(streamEventMsg{
		Event: runtime.Event{Type: runtime.EventThinking}, // empty payload: turn start marker only
		gen:   1,
	})
	next, _ = next.(model).Update(streamEventMsg{
		Event: runtime.Event{Type: runtime.EventText, Text: "plain answer, no reasoning"},
		gen:   1,
	})
	got := next.(model)

	for _, e := range got.entries {
		if e.Thinking != nil {
			t.Errorf("unexpected Thinking entry for a provider that sent no reasoning: %+v", e.Thinking)
		}
	}
}

func TestClearResetsStreamState(t *testing.T) {
	m := sizedModel()
	m.streamGen = 3
	m.waiting = true
	m.assistantBuffer = "leftover"
	m.activeTools = map[string]int{"tool-1": 0}
	m.entries = []chatEntry{{Role: roleUser, Content: "hi"}}

	m.Clear()

	if m.waiting {
		t.Error("Clear left waiting set")
	}
	if m.assistantBuffer != "" {
		t.Error("Clear left assistantBuffer set")
	}
	if len(m.activeTools) != 0 {
		t.Error("Clear left activeTools populated")
	}
	if m.streamGen != 4 {
		t.Errorf("streamGen = %d, want incremented past the cleared stream", m.streamGen)
	}
}

func TestShortResultTruncatesOnRuneBoundaries(t *testing.T) {
	multibyte := strings.Repeat("é", 200) // 2 bytes each, 400 bytes total
	got := shortResult(multibyte)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("shortResult missing ellipsis: %q", got)
	}
	for _, r := range got {
		if r == 0xFFFD {
			t.Fatalf("shortResult split a multi-byte rune: %q", got)
		}
	}
}

func TestToggleToolExpansion(t *testing.T) {
	m := sizedModel()
	m.startToolActivity(&providers.ToolCall{ID: "t1", Name: "shell", Arguments: map[string]any{"command": "go test ./..."}})
	fillToolRecord(m.entries[0].Tool, &runtime.ToolResult{
		ToolCallID: "t1", Name: "shell", Success: true, Content: "ok",
		Stdout: "ok", ExitCode: 0, HasExitCode: true, Duration: 1500000000,
	}, runtime.EventToolFinish)

	rendered := m.entries[0].render(m.viewport.Width, false)
	if strings.Contains(rendered, "exit code") {
		t.Errorf("tool details visible while compact: %q", rendered)
	}

	m.toggleToolExpansion()
	rendered = m.entries[0].render(m.viewport.Width, false)
	for _, want := range []string{"command:", "go test ./...", "exit code: 0", "stdout:", "duration:"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("expanded view missing %q:\n%s", want, rendered)
		}
	}

	m.toggleToolExpansion()
	rendered = m.entries[0].render(m.viewport.Width, false)
	if strings.Contains(rendered, "exit code") {
		t.Errorf("tool details still visible after collapsing: %q", rendered)
	}
}

func TestClampLinesMarksHiddenLines(t *testing.T) {
	lines := clampLines(strings.Repeat("x\n", 100), maxExpandedOutputLines)
	if len(lines) != maxExpandedOutputLines+1 {
		t.Fatalf("clampLines returned %d lines, want %d+1", len(lines), maxExpandedOutputLines)
	}
	if !strings.Contains(lines[len(lines)-1], "+61 more lines") {
		t.Errorf("hidden-line marker = %q", lines[len(lines)-1])
	}
}

func TestOsc52Sequence(t *testing.T) {
	seq := osc52Sequence("hello")
	if !strings.HasPrefix(seq, "\x1b]52;c;") || !strings.HasSuffix(seq, "\x07") {
		t.Errorf("osc52Sequence malformed: %q", seq)
	}
	if !strings.Contains(seq, "aGVsbG8=") {
		t.Errorf("osc52Sequence missing base64 payload: %q", seq)
	}
}

func TestEscClearsInputBeforeQuitting(t *testing.T) {
	m := sizedModel()
	next, _ := m.handleKey(pasteMsg("draft message"))
	got := next.(model)

	next, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	got = next.(model)
	if got.quitting {
		t.Error("Esc quit while the input held a draft; it should clear the draft first")
	}
	if got.input.Value() != "" {
		t.Errorf("input.Value() = %q, want cleared", got.input.Value())
	}

	next, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	got = next.(model)
	if !got.quitting {
		t.Error("Esc with an empty input should quit")
	}
}

func TestCtrlT_TogglesActivityStatus(t *testing.T) {
	m := sizedModel()
	m.waiting = true
	m.status = "Thinking"

	if !strings.Contains(m.renderFooter(), "Thinking") {
		t.Error("activity status not shown by default")
	}

	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	got := next.(model)
	if got.showActivity {
		t.Error("ctrl+t did not disable activity status")
	}
	if strings.Contains(got.renderFooter(), "Thinking") {
		t.Error("activity status still rendered after ctrl+t")
	}
}

func TestResizeKeepsTranscriptAndLayoutSane(t *testing.T) {
	m := sizedModel()
	for i := 0; i < 30; i++ {
		m.entries = append(m.entries, chatEntry{Role: roleUser, Content: strings.Repeat("x", 30)})
	}
	m.viewport.GotoBottom()

	next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	got := next.(model)

	if got.viewport.Width != 40 {
		t.Errorf("viewport.Width = %d, want 40", got.viewport.Width)
	}
	if got.viewport.Height < 1 || got.viewport.Height > 10 {
		t.Errorf("viewport.Height = %d out of range", got.viewport.Height)
	}
	if !strings.Contains(got.viewport.View(), "xxx") {
		t.Error("transcript content lost after resize")
	}
}

func TestStreamingPartialDiagramDoesNotBreakRender(t *testing.T) {
	m := sizedModel()
	full := "Here is the flow:\n\n" + asciiFlowchart
	for i := 0; i < len(full); i += 7 {
		end := min(i+7, len(full))
		m.appendAssistantText(full[i:end])
		m.refreshTranscript()
	}
	m.finishAssistantStream()
	m.refreshTranscript()
	rendered := m.viewport.View()
	for _, line := range strings.Split(asciiFlowchart, "\n") {
		if !strings.Contains(rendered, strings.TrimRight(line, " ")) {
			t.Errorf("final render lost flowchart line %q", line)
		}
	}
}
