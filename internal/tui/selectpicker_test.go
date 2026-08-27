package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

func TestProviderPickerRendersCapabilityDetails(t *testing.T) {
	summaries := []runtime.ProviderSummary{
		{ID: "ollama", Name: "Ollama", Detail: "local · tools · streaming · reasoning", Models: []string{"ornith:9b"}, Available: true},
		{ID: "openai", Name: "OpenAI", Detail: "cloud · tools · streaming · api key missing", Models: []string{"gpt-4o-mini"}, Available: false},
	}

	picker := newSelectPicker("Provider", providerOptions(summaries, "ollama"), scopeProvider)
	box := picker.box()

	if !strings.Contains(box, "Ollama") || !strings.Contains(box, "local · tools · streaming · reasoning") {
		t.Errorf("picker missing the Ollama row or its detail:\n%s", box)
	}
	if !strings.Contains(box, "cloud · tools · streaming · api key missing") {
		t.Errorf("picker missing the OpenAI availability detail:\n%s", box)
	}
	if !strings.Contains(box, "✓") {
		t.Errorf("picker does not mark the current provider:\n%s", box)
	}

	// Detail rows occupy two terminal rows each, so the option band for
	// the second provider starts at row 4 (2 options x 2 rows from top).
	bx, by := picker.boxOrigin(120, 60)
	idx, ok := picker.rowAt(bx+5, by+pickerRowsTop+3, 120, 60)
	if !ok || idx != 1 {
		t.Errorf("rowAt(detail row) = %d %v, want option 1", idx, ok)
	}
}

func TestModelPickerUsesFriendlyNamesAndCurrentMark(t *testing.T) {
	models := []providers.Model{
		{ID: "z-ai/glm-5.2", Name: "GLM 5.2", Provider: "nvidia"},
		{ID: "thinkingmachines/inkling", Name: "Inkling", Provider: "nvidia"},
	}

	options := modelOptions(models, "z-ai/glm-5.2", runtime.ModelsUnsupported)
	if len(options) != 2 {
		t.Fatalf("options = %d, want two (no refresh row when unsupported)", len(options))
	}
	if options[0].Label != "GLM 5.2" {
		t.Errorf("label = %q, want the friendly GLM 5.2 name", options[0].Label)
	}
	if !options[0].Current || options[1].Current {
		t.Errorf("current marks = %v/%v, want only glm-5.2 marked", options[0].Current, options[1].Current)
	}

	withRefresh := modelOptions(models, "", runtime.ModelsFresh)
	if len(withRefresh) != 3 || withRefresh[len(withRefresh)-1].ID != refreshOptionID {
		t.Errorf("refresh row missing for discoverable provider: %+v", withRefresh)
	}
}

func TestLongModelNamesAreTruncated(t *testing.T) {
	longName := strings.Repeat("very-long-model-name-", 10) // > 200 chars
	models := []providers.Model{
		{ID: "long", Name: longName, Provider: "lab"},
	}
	options := modelOptions(models, "long", runtime.ModelsUnsupported)
	picker := newSelectPicker("Model", options, scopeModel)
	box := picker.boxForHeight(30)
	if strings.Contains(box, longName) {
		t.Errorf("box contains untruncated long name (%d chars), expected ellipsis", len(longName))
	}
	if !strings.Contains(box, "…") {
		t.Errorf("box does not contain ellipsis for truncated name:\n%s", box)
	}
	// Rendered width of the row should not exceed pickerWidth
	for _, line := range strings.Split(box, "\n") {
		stripped := stripANSI(line)
		if len(stripped) > 0 && lipglossWidth(stripped) > pickerWidth+2 { // allow border slack
			t.Errorf("rendered line too wide (%d > %d): %q", lipglossWidth(stripped), pickerWidth, stripped)
		}
	}
}

func TestPickerScrollingKeepsCursorVisible(t *testing.T) {
	// 30 short models, no details (1 row each), small terminal height
	var models []providers.Model
	for i := 0; i < 30; i++ {
		id := strings.Repeat("m", 2) + strings.Repeat("0", 2) + string(rune('a'+i%26)) // varying but short
		models = append(models, providers.Model{ID: id, Name: id, Provider: "lab"})
	}
	options := modelOptions(models, "", runtime.ModelsUnsupported)
	picker := newSelectPicker("Model", options, scopeModel)
	height := 24 // small terminal, mimics typical laptop

	// Initially cursor 0 visible, offset 0
	picker.ensureVisible(height)
	if picker.offset != 0 {
		t.Errorf("initial offset = %d, want 0", picker.offset)
	}

	// Move cursor far down and ensure it scrolls
	for i := 0; i < 20; i++ {
		picker.moveDown()
		picker.ensureVisible(height)
	}
	if picker.cursor != 20 {
		t.Fatalf("cursor = %d, want 20", picker.cursor)
	}
	if picker.offset == 0 {
		t.Errorf("offset still 0 after moving cursor to 20, expected scrolling")
	}
	// Cursor should be within visible window
	start, end := picker.visibleRange(picker.maxVisibleRows(height))
	if picker.cursor < start || picker.cursor >= end {
		t.Errorf("cursor %d not in visible window [%d,%d) after scrolling", picker.cursor, start, end)
	}

	// Box should show scroll indicators
	box := picker.boxForHeight(height)
	if !strings.Contains(box, "more above") && !strings.Contains(box, "more") {
		t.Errorf("expected scroll indicator when list is scrolled:\n%s", box)
	}
	if strings.Contains(box, "more below") || strings.Contains(box, "more") {
		// should have "more" when not all visible - we expect at least one indicator
	}

	// Scroll back to top
	for i := 0; i < 20; i++ {
		picker.moveUp()
		picker.ensureVisible(height)
	}
	if picker.cursor != 0 || picker.offset != 0 {
		t.Errorf("after scrolling back: cursor=%d offset=%d, want 0/0", picker.cursor, picker.offset)
	}

	// Click mapping should still work when scrolled: second visible option's row
	picker.cursor = 15
	picker.offset = 10
	picker.heights = nil // force recalc
	picker.ensureVisible(height)
	bx, by := picker.boxOrigin(80, height)
	// first visible is offset=10, its label row at y = by+selectRowsTop (+ indicator if offset>0)
	// indicator adds 1 row, so first visible at y+1
	y := by + pickerRowsTop + 1 // first visible option's first row when scrolled (up indicator occupies first row)
	idx, ok := picker.rowAt(bx+5, y, 80, height)
	if !ok || idx != 10 {
		t.Errorf("rowAt with offset 10: idx=%d ok=%v, want 10 true", idx, ok)
	}
}

// Helpers for the new tests.
func lipglossWidth(s string) int {
	return lipgloss.Width(s)
}
