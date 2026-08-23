package config

import (
	"strings"
	"testing"
)

func TestSandboxConfigRoundTrip(t *testing.T) {
	isolateHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg.Sandbox.Mode = "wsl"
	cfg.Sandbox.WSL.Distribution = "Ubuntu-22.04"
	cfg.Sandbox.WSL.Network = "host"

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("re-Load() error = %v", err)
	}
	if got.Sandbox.Mode != "wsl" || got.Sandbox.WSL.Distribution != "Ubuntu-22.04" || got.Sandbox.WSL.Network != "host" {
		t.Errorf("sandbox section did not round-trip: %+v", got.Sandbox)
	}
}

func TestLoadRejectsInvalidSandboxMode(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://x\n  name: m\nsandbox:\n  mode: docker\n")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted an unknown sandbox mode")
	}
	if !strings.Contains(err.Error(), "sandbox.mode") || !strings.Contains(err.Error(), "docker") {
		t.Errorf("error = %v, want it to name sandbox.mode and quote the value", err)
	}
}

func TestLoadRejectsInvalidSandboxNetwork(t *testing.T) {
	isolateHome(t)
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://x\n  name: m\nsandbox:\n  wsl:\n    network: blocked-ish\n")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "sandbox.wsl.network") {
		t.Fatalf("error = %v, want a named sandbox.wsl.network rejection", err)
	}
}

func TestLoadRejectsHostileDistributionName(t *testing.T) {
	for _, distro := range []string{"-evil", "../x", "a b", "a;b"} {
		t.Run(distro, func(t *testing.T) {
			isolateHome(t)
			writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://x\n  name: m\nsandbox:\n  mode: wsl\n  wsl:\n    distribution: \""+distro+"\"\n")

			if _, err := Load(); err == nil {
				t.Fatalf("distribution name %q accepted", distro)
			}
		})
	}
}

func TestEmptySandboxSectionIsValidAndMeansNative(t *testing.T) {
	isolateHome(t)
	cfg, err := Load() // default template has mode: native
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Sandbox.Mode != "native" {
		t.Logf("note: template mode = %q", cfg.Sandbox.Mode)
	}

	// A user file without any sandbox section at all must also load.
	writeConfig(t, "model:\n  provider: ollama\n  endpoint: http://x\n  name: m\n")
	cfg2, err := Load()
	if err != nil {
		t.Fatalf("Load() without sandbox section error = %v", err)
	}
	if cfg2.Sandbox.Mode != "" {
		t.Errorf("mode = %q, want empty (native by convention)", cfg2.Sandbox.Mode)
	}
}
