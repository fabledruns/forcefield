package tui

import (
	"strings"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// The provider picker renders whatever summaries the runtime hands it,
// one row per summary. The runtime now emits setup rows for cloud
// presets that failed resolution; pin that the picker neither filters
// nor rewords them.
func TestProviderPickerShowsSetupRowsVerbatim(t *testing.T) {
	summaries := []runtime.ProviderSummary{
		{ID: "ollama", Name: "Ollama", Detail: "local · tools · streaming · reasoning", Models: []string{"ornith:9b"}, Available: true},
		{ID: "vertex", Name: "Google Vertex AI", Detail: "setup needed · set providers.vertex.project", Available: false},
		{ID: "azure", Name: "Azure AI", Detail: "setup needed · set base_url under providers.azure", Available: false},
	}

	picker := newSelectPicker("Provider", providerOptions(summaries, "ollama"), scopeProvider)
	box := picker.box()

	if len(picker.options) != len(summaries) {
		t.Fatalf("options = %d, want one row per summary with no filtering", len(picker.options))
	}
	for _, want := range []string{
		"Google Vertex AI",
		"setup needed · set providers.vertex.project",
		"Azure AI",
		"setup needed · set base_url under providers.azure",
	} {
		if !strings.Contains(box, want) {
			t.Errorf("picker missing %q:\n%s", want, box)
		}
	}
}

// Switching to a resolved provider with an empty model list and no
// discovery for its transport must state the next step instead of
// silently keeping the previous model. Bedrock is the concrete case: it
// does not require an API key (the AWS chain owns auth), it resolves
// with a region alone, it has no listing probe, and it ships no
// fallback models by design.
func TestChooseProviderWithoutModelsExplainsNextStep(t *testing.T) {
	isolateModelHome(t)
	writeModelConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: ornith:9b\n\n"+
		"providers:\n  br:\n    type: bedrock\n    region: us-east-1\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}

	next, _ := m.chooseProvider("br")
	nextModel, ok := next.(model)
	if !ok {
		t.Fatalf("chooseProvider returned %T, want model", next)
	}

	if nextModel.providerName != "br" {
		t.Errorf("provider = %q, want br after switching", nextModel.providerName)
	}
	if nextModel.selectPicker != nil {
		t.Errorf("model picker opened for a provider with no models; want a chat-side explanation instead")
	}
	found := false
	for _, e := range nextModel.entries {
		if strings.Contains(e.Content, "providers.br.models") {
			found = true
		}
	}
	if !found {
		t.Errorf("transcript lacks the add-providers.br.models guidance:\n%v", nextModel.entries)
	}
}

// A single configured default model (providers.<id>.model with no
// models list) is provider-owned model content: after the switch it is
// selectable, so the "no models listed" guidance must not appear next
// to it. The configured id doubles as the active model so the
// carried-over selection and the entry default dedupe to exactly one
// option, exercising the single-model auto-pick path on the Bedrock
// transport (no discovery, no fallback models).
func TestChooseProviderWithDefaultModelSkipsListGuidance(t *testing.T) {
	isolateModelHome(t)
	writeModelConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: anthropic.claude-3-7-sonnet-v1:0\n\n"+
		"providers:\n  br:\n    type: bedrock\n    region: us-east-1\n    model: anthropic.claude-3-7-sonnet-v1:0\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	m, err := newModel(cfg, session.New(), &tuiAsker{})
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}

	next, _ := m.chooseProvider("br")
	nextModel, ok := next.(model)
	if !ok {
		t.Fatalf("chooseProvider returned %T, want model", next)
	}

	if nextModel.providerName != "br" {
		t.Errorf("provider = %q, want br after switching", nextModel.providerName)
	}
	if nextModel.modelName != "anthropic.claude-3-7-sonnet-v1:0" {
		t.Errorf("model = %q, want the entry default selected", nextModel.modelName)
	}
	for _, e := range nextModel.entries {
		if strings.Contains(e.Content, "no models listed") {
			t.Errorf("guidance shown despite a configured default model:\n%s", e.Content)
		}
	}
}
