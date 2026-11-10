//go:build windows

package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureWindowsTerminalFragment(t *testing.T) {
	tempLocal := t.TempDir()
	origLocal := os.Getenv("LOCALAPPDATA")
	defer os.Setenv("LOCALAPPDATA", origLocal)
	os.Setenv("LOCALAPPDATA", tempLocal)

	// Case 1: Fragments directory does not exist (WT not installed) -> graceful no-op
	err := EnsureWindowsTerminalFragment("")
	if err != nil {
		t.Fatalf("expected no error when WT not installed, got: %v", err)
	}

	// Case 2: Fragments directory exists -> installs fragment and copies logo
	fragmentsBase := filepath.Join(tempLocal, "Microsoft", "Windows Terminal", "Fragments")
	if err := os.MkdirAll(fragmentsBase, 0o755); err != nil {
		t.Fatalf("failed to create fake fragments dir: %v", err)
	}

	// Create fake logo
	fakeLogo := filepath.Join(t.TempDir(), "test_logo.png")
	if err := os.WriteFile(fakeLogo, []byte("fake png content"), 0o644); err != nil {
		t.Fatalf("failed to write fake logo: %v", err)
	}

	if err := EnsureWindowsTerminalFragment(fakeLogo); err != nil {
		t.Fatalf("EnsureWindowsTerminalFragment failed: %v", err)
	}

	fragFile := filepath.Join(fragmentsBase, "Forcefield", "forcefield.json")
	data, err := os.ReadFile(fragFile)
	if err != nil {
		t.Fatalf("expected forcefield.json to exist, err: %v", err)
	}

	var parsed wtFragment
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid json in fragment: %v", err)
	}

	if len(parsed.Profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(parsed.Profiles))
	}
	p := parsed.Profiles[0]
	if p.Name != "Forcefield" {
		t.Errorf("expected profile name Forcefield, got %q", p.Name)
	}
	if p.CommandLine != "ff" {
		t.Errorf("expected commandline ff, got %q", p.CommandLine)
	}

	logoDest := filepath.Join(fragmentsBase, "Forcefield", "logo.png")
	logoBytes, err := os.ReadFile(logoDest)
	if err != nil {
		t.Fatalf("expected logo.png copied, err: %v", err)
	}
	if string(logoBytes) != "fake png content" {
		t.Errorf("logo content mismatch, got %q", string(logoBytes))
	}
}
