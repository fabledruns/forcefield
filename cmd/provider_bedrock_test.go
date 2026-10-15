package cmd

import (
	"fmt"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func TestProviderTestBedrockValidationOnly(t *testing.T) {
	isolateProviderHome(t)
	// No network is touched: there is no models-listing probe for
	// Bedrock, so the test validates configuration and describes the
	// credential chain without billing a call.
	writeProviderConfig(t, "model:\n  provider: bb\n  name: test-model\nproviders:\n  bb:\n    type: bedrock\n    region: us-east-1\n    models:\n      - test-model\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := runProviderTest(cfg, "bb", false, 0); err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
}

func TestProviderTestBedrockWarnsWithoutModels(t *testing.T) {
	isolateProviderHome(t)
	writeProviderConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\nproviders:\n  bb:\n    type: bedrock\n    region: us-east-1\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// Passes (validation only) even without configured models; the
	// warning is informational.
	if err := runProviderTest(cfg, "bb", false, 0); err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
}

func TestProviderTestBedrockMissingRegion(t *testing.T) {
	isolateProviderHome(t)
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	writeProviderConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\nproviders:\n  bb:\n    type: bedrock\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v (bedrock is not active)", err)
	}
	err = runProviderTest(cfg, "bb", false, 0)
	if err == nil {
		t.Fatal("runProviderTest accepted bedrock with no region")
	}
	if !strings.Contains(err.Error(), "region") {
		t.Errorf("error = %q, want it to name region", err)
	}
}

func TestDoctorBedrockGuidance(t *testing.T) {
	isolateProviderHome(t)
	writeProviderConfig(t, "model:\n  provider: bb\n  name: m\nproviders:\n  bb:\n    type: bedrock\n    region: us-west-2\n    profile: work\n    models:\n      - m\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("bb", "m")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	var reports []string
	report := func(v verdict, format string, args ...any) {
		reports = append(reports, fmt.Sprintf(format, args...))
	}
	doctorBedrock(resolved, report)
	joined := strings.Join(reports, "\n")
	if !strings.Contains(joined, "us-west-2") || !strings.Contains(joined, "work") {
		t.Errorf("reports = %q, want region and profile named", joined)
	}
}
