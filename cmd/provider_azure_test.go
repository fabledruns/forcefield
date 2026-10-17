package cmd

import (
	"fmt"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func TestProviderTestAzureValidationOnly(t *testing.T) {
	isolateProviderHome(t)
	// No network is touched: there is no models-listing probe for
	// Azure, so the test validates configuration and describes the
	// auth path without billing a call.
	writeProviderConfig(t, "model:\n  provider: az\n  name: gpt4o-deploy\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    api_key_env: AZURE_OPENAI_API_KEY\n    models:\n      - gpt4o-deploy\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := runProviderTest(cfg, "az", false, 0); err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
}

func TestProviderTestAzureCustomAPIVersion(t *testing.T) {
	isolateProviderHome(t)
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")
	writeProviderConfig(t, "model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    api_version: 2025-01-01-preview\n    models:\n      - m\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := runProviderTest(cfg, "az", false, 0); err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
}

func TestProviderTestAzureEntraMode(t *testing.T) {
	isolateProviderHome(t)
	t.Setenv("AZURE_ENTRA_TOKEN", "entra-token")
	writeProviderConfig(t, "model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    auth_mode: bearer\n    api_key_env: AZURE_ENTRA_TOKEN\n    models:\n      - m\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := runProviderTest(cfg, "az", false, 0); err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
}

func TestProviderTestAzureMissingBaseURL(t *testing.T) {
	isolateProviderHome(t)
	// Azure is not the active provider, so Load passes; the test
	// command must still name the missing resource endpoint.
	writeProviderConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\nproviders:\n  az:\n    type: azure\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v (azure is not active)", err)
	}
	err = runProviderTest(cfg, "az", false, 0)
	if err == nil {
		t.Fatal("runProviderTest accepted azure with no base_url")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Errorf("error = %q, want it to name base_url", err)
	}
}

func TestProviderTestAzureChatIgnored(t *testing.T) {
	isolateProviderHome(t)
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")
	writeProviderConfig(t, "model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    models:\n      - m\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// --chat never reaches the probing path for azure (like vertex and
	// bedrock): the validation-only branch returns first, so no
	// billable completion is sent.
	if err := runProviderTest(cfg, "az", true, 0); err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
}

func TestDoctorAzureGuidance(t *testing.T) {
	isolateProviderHome(t)
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")
	writeProviderConfig(t, "model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    api_version: 2025-01-01-preview\n    models:\n      - m\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("az", "m")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	var reports []string
	report := func(v verdict, format string, args ...any) {
		reports = append(reports, fmt.Sprintf(format, args...))
	}
	doctorAzure(resolved, report)
	joined := strings.Join(reports, "\n")
	if !strings.Contains(joined, "myres.openai.azure.com") {
		t.Errorf("reports = %q, want the resource endpoint named", joined)
	}
	if !strings.Contains(joined, "2025-01-01-preview") {
		t.Errorf("reports = %q, want the api-version named", joined)
	}
}

func TestDoctorAzureEntraMode(t *testing.T) {
	isolateProviderHome(t)
	t.Setenv("AZURE_ENTRA_TOKEN", "entra-token")
	writeProviderConfig(t, "model:\n  provider: az\n  name: m\nproviders:\n  az:\n    type: azure\n    base_url: https://myres.openai.azure.com\n    auth_mode: bearer\n    api_key_env: AZURE_ENTRA_TOKEN\n    models:\n      - m\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("az", "m")
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	var reports []string
	report := func(v verdict, format string, args ...any) {
		reports = append(reports, fmt.Sprintf(format, args...))
	}
	doctorAzure(resolved, report)
	joined := strings.Join(reports, "\n")
	if !strings.Contains(joined, "Entra") {
		t.Errorf("reports = %q, want Entra named", joined)
	}
	if strings.Contains(joined, "entra-token") {
		t.Errorf("reports = %q, must not contain the token value", joined)
	}
}
