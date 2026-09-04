package tui

import (
	"strings"
	"testing"
	"time"

	"forcefield/internal/runtime"
)

// turnEntries builds one assistant turn's worth of entries sharing a turn
// id: text, two tool results, thinking, then another tool result.
func turnEntries(turn uint64) []chatEntry {
	return []chatEntry{
		{Role: roleAssistant, Content: "working on it", Turn: turn},
		{Role: roleActivity, Content: "✳ Found 11 entries", Turn: turn, Tool: &toolRecord{name: "list_files", finished: true, eventType: runtime.EventToolFinish}},
		{Role: roleActivity, Content: "◈ Read go.mod", Turn: turn, Tool: &toolRecord{name: "read_file", finished: true, eventType: runtime.EventToolFinish}},
		{Role: roleActivity, Turn: turn, Thinking: &thinkingRecord{text: "hmm", startedAt: time.Now(), endedAt: time.Now()}},
		{Role: roleActivity, Content: "✳ Found 13 entries", Turn: turn, Tool: &toolRecord{name: "search_files", finished: true, eventType: runtime.EventToolFinish}},
	}
}

func TestGroupedTurnRendersOneHeader(t *testing.T) {
	entries := append([]chatEntry{{Role: roleUser, Content: "go"}}, turnEntries(7)...)
	content, _ := renderTranscriptWithLayout(entries, 80, "")
	plain := stripANSI(content)
	if got := strings.Count(plain, "Forcefield"); got != 1 {
		t.Errorf("grouped turn has %d Forcefield headers, want 1:\n%s", got, plain)
	}
}

func TestGroupedTurnHasNoBlankPadding(t *testing.T) {
	entries := append([]chatEntry{{Role: roleUser, Content: "go"}}, turnEntries(7)...)
	content, _ := renderTranscriptWithLayout(entries, 80, "")
	lines := strings.Split(stripANSI(content), "\n")
	// Between member blocks there must be no blank separator rows. The
	// assistant body ("working on it") is followed directly by the
	// first tool row, and every row after it up to the end must be
	// content: glamour's pre-existing top margin under the Forcefield
	// label is the only blank allowed, and it sits above the body.
	body := -1
	for i, l := range lines {
		if strings.Contains(l, "working on it") {
			body = i
			break
		}
	}
	if body < 0 {
		t.Fatalf("assistant body missing:\n%s", strings.Join(lines, "\n"))
	}
	for i := body; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			t.Errorf("blank row %d between group members:\n%s", i, strings.Join(lines, "\n"))
		}
	}
}

func TestSeparateTurnsKeepSeparation(t *testing.T) {
	entries := []chatEntry{{Role: roleUser, Content: "go"}}
	entries = append(entries, turnEntries(7)...)
	entries = append(entries, turnEntries(9)...)
	content, _ := renderTranscriptWithLayout(entries, 80, "")
	plain := stripANSI(content)
	if got := strings.Count(plain, "Forcefield"); got != 2 {
		t.Errorf("two turns have %d headers, want 2:\n%s", got, plain)
	}
	if !strings.Contains(plain, "\n\n") {
		t.Errorf("separate turns lost blank separation:\n%s", plain)
	}
}

func TestUngroupedEntriesRenderAsBefore(t *testing.T) {
	entries := []chatEntry{
		{Role: roleUser, Content: "go"},
		{Role: roleAssistant, Content: "first"},
		{Role: roleAssistant, Content: "second"},
	}
	content, _ := renderTranscriptWithLayout(entries, 80, "")
	plain := stripANSI(content)
	if got := strings.Count(plain, "Forcefield"); got != 2 {
		t.Errorf("ungrouped assistant entries have %d headers, want 2:\n%s", got, plain)
	}
	if !strings.Contains(plain, "\n\n") {
		t.Errorf("ungrouped entries lost blank separation:\n%s", plain)
	}
}

func TestGroupedSpansMatchRenderedRows(t *testing.T) {
	entries := append([]chatEntry{{Role: roleUser, Content: "go"}}, turnEntries(7)...)
	content, spans := renderTranscriptWithLayout(entries, 80, "")
	lines := strings.Split(stripANSI(content), "\n")
	if len(spans) != 4 { // 3 tools + 1 thinking
		t.Fatalf("got %d spans, want 4", len(spans))
	}
	for _, s := range spans {
		if s.startLine >= len(lines) {
			t.Fatalf("span %+v starts past end (%d lines)", s, len(lines))
		}
		row := stripANSI(lines[s.startLine])
		switch s.action {
		case actionToggleTool:
			want := entries[s.entry].Content
			if !strings.Contains(row, want) {
				t.Errorf("tool span entry %d starts on %q, want %q", s.entry, row, want)
			}
		case actionToggleThinking:
			if !strings.Contains(row, "Thinking") {
				t.Errorf("thinking span entry %d starts on %q", s.entry, row)
			}
		}
	}
}

func TestStreamingDoesNotDuplicateHeaders(t *testing.T) {
	m := newTestModel()
	m.streamGen = 3
	// Simulate a live run: text, tool start/finish, thinking, more text.
	m.appendAssistantText("checking ")
	m.startToolActivity(nil)
	m.entries[len(m.entries)-1].Turn = m.streamGen
	m.finishToolActivity(&runtime.ToolResult{ToolCallID: "", Name: "read_file", Content: "x", Success: true}, runtime.EventToolFinish)
	m.appendThinking("reasoning")
	m.finishThinkingStream()
	m.appendAssistantText("done")
	m.viewport.Width = 80
	m.refreshTranscript()
	plain := stripANSI(m.viewport.View())
	_ = plain
	content := stripANSI(m.tcacheContent)
	if got := strings.Count(content, "Forcefield"); got != 1 {
		t.Errorf("streaming run has %d headers, want 1:\n%s", got, content)
	}
}

func TestThinkingHeaderUsesDiamond(t *testing.T) {
	out := stripANSI(renderThinking(&thinkingRecord{text: "", startedAt: time.Now(), endedAt: time.Now()}, 80, false))
	if !strings.Contains(out, "◇ Thinking") {
		t.Errorf("thinking header = %q, want ◇ Thinking", out)
	}
	if strings.Contains(out, "✓") {
		t.Errorf("thinking header must not use ✓: %q", out)
	}
}

func TestToolRowsUseMutedStyle(t *testing.T) {
	// Successful rows must not carry the old bright-green status color.
	e := chatEntry{Role: roleActivity, Content: "◈ Read go.mod", Tool: &toolRecord{name: "read_file", finished: true, eventType: runtime.EventToolFinish}}
	rendered := e.render(80, false)
	if strings.Contains(rendered, string(colorSuccess)) {
		t.Errorf("success row still uses green: %q", rendered)
	}
}
