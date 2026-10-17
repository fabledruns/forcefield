package cmd

import (
	"testing"

	"forcefield/internal/config"
)

// TestProviderAddRejectsEntriesLoadWouldReject pins that `ff provider add`
// applies the same entry validation config.Load applies (validateEntry).
// Without it, add can persist a providers entry that makes every
// subsequent Load fail - and the whole CLI refuses to start - because
// Save itself never validates.
func TestProviderAddRejectsEntriesLoadWouldReject(t *testing.T) {
	cases := []struct {
		name  string
		setup func()
	}{
		{"azure api_path", func() {
			providerAddName, providerAddType = "az", "azure"
			providerAddBaseURL = "https://myres.openai.azure.com"
			providerAddAPIPath = "/v1/custom/chat"
		}},
		{"azure auth_header", func() {
			providerAddName, providerAddType = "az", "azure"
			providerAddBaseURL = "https://myres.openai.azure.com"
			providerAddAuthMode, providerAddAuthHdr = "header", "X-Custom"
		}},
		{"azure region", func() {
			providerAddName, providerAddType = "az", "azure"
			providerAddBaseURL = "https://myres.openai.azure.com"
			providerAddRegion = "us-east-1"
		}},
		{"api_version on openai-compatible", func() {
			providerAddName, providerAddType = "oa", "openai-compatible"
			providerAddBaseURL = "http://localhost:1234/v1"
			providerAddAPIVersion = "2024-06-01"
		}},
		{"vertex api_path", func() {
			providerAddName, providerAddType = "vx", "vertex"
			providerAddProject = "my-proj"
			providerAddLocation = "us-central1"
			providerAddAPIPath = "/v1/custom/chat"
		}},
		{"vertex auth_mode", func() {
			providerAddName, providerAddType = "vx", "vertex"
			providerAddProject = "my-proj"
			providerAddLocation = "us-central1"
			providerAddAuthMode = "bearer"
		}},
		{"bedrock project", func() {
			providerAddName, providerAddType = "br", "bedrock"
			providerAddRegion = "us-east-1"
			providerAddProject = "my-proj"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateProviderHome(t)
			writeProviderConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n")
			tc.setup()
			if err := providerAddCmd.RunE(providerAddCmd, nil); err == nil {
				t.Fatal("provider add accepted an entry that config.Load would reject")
			}
			// A rejected entry must not be persisted: the config must
			// still load afterwards.
			if _, err := config.Load(); err != nil {
				t.Fatalf("Load() error = %v - add persisted an unloadable entry", err)
			}
		})
	}
}

// TestProviderAddAzureValidSavesLoadableConfig guards the other side of
// the validation gate: a legitimate azure entry (deployment models plus a
// custom api-version) still saves and loads.
func TestProviderAddAzureValidSavesLoadableConfig(t *testing.T) {
	isolateProviderHome(t)
	writeProviderConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n")

	providerAddName, providerAddType = "az", "azure"
	providerAddBaseURL = "https://myres.openai.azure.com"
	providerAddAPIVersion = "2025-01-01-preview"
	providerAddModels = []string{"gpt4o-deploy"}
	if err := providerAddCmd.RunE(providerAddCmd, nil); err != nil {
		t.Fatalf("add error = %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	entry, ok := cfg.Providers["az"]
	if !ok {
		t.Fatal("added azure provider missing from config")
	}
	if entry.APIVersion != "2025-01-01-preview" || len(entry.Models) != 1 {
		t.Errorf("entry = %+v, want api_version and models preserved", entry)
	}
}
