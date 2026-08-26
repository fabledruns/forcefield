package tui

import (
	"strings"
	"testing"

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
	summaries := []runtime.ProviderSummary{
		{ID: "nvidia", Name: "NVIDIA NIM", Detail: "cloud · tools", Models: []string{"z-ai/glm-5.2", "thinkingmachines/inkling"}, Available: true},
	}

	options := modelOptions(summaries, "nvidia", "z-ai/glm-5.2")
	if len(options) != 2 {
		t.Fatalf("options = %d, want two", len(options))
	}
	if options[0].Label != "GLM 5.2" {
		t.Errorf("label = %q, want the friendly GLM 5.2 name", options[0].Label)
	}
	if !options[0].Current || options[1].Current {
		t.Errorf("current marks = %v/%v, want only glm-5.2 marked", options[0].Current, options[1].Current)
	}
}
