package cmd

import (
	"strings"
	"testing"

	"forcefield/internal/config"
)

func TestProviderTestVertexValidationOnly(t *testing.T) {
	isolateProviderHome(t)
	// No network is touched: vertex has no models-listing probe, so the
	// test validates configuration and describes ADC without billing.
	writeProviderConfig(t, "model:\n  provider: vtx\n  name: gemini-2.5-flash\nproviders:\n  vtx:\n    type: vertex\n    project: my-proj\n    location: us-central1\n    models:\n      - gemini-2.5-flash\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := runProviderTest(cfg, "vtx", false, 0); err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
}

func TestProviderTestVertexMissingLocation(t *testing.T) {
	isolateProviderHome(t)
	writeProviderConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\nproviders:\n  vtx:\n    type: vertex\n    project: my-proj\n")

	// Fail-closed at load: location is required for vertex entries.
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() accepted vertex without location")
	}
	if !strings.Contains(err.Error(), "location") {
		t.Errorf("error = %q, want it to name location", err)
	}
}
