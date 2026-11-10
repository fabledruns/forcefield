//go:build windows

package terminal

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

type wtProfile struct {
	GUID        string `json:"guid"`
	Name        string `json:"name"`
	CommandLine string `json:"commandline"`
	Icon        string `json:"icon"`
}

type wtFragment struct {
	Profiles []wtProfile `json:"profiles"`
}

const forcefieldProfileGUID = "{e3d5a41a-8f19-4827-88d4-5182903bb222}"

// findLogoPath searches for assets/logo.png relative to cwd or executable.
func findLogoPath() string {
	candidates := []string{
		"assets/logo.png",
		filepath.Join(".", "assets", "logo.png"),
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "assets", "logo.png"))
	}

	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}

// EnsureWindowsTerminalFragment installs or updates the Windows Terminal JSON fragment
// extension for Forcefield so that Windows Terminal tabs opened for Forcefield display logo.png.
// It is non-destructive and never modifies settings.json or user shell profiles.
func EnsureWindowsTerminalFragment(logoPath string) error {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return nil
	}

	fragmentsBase := filepath.Join(localAppData, "Microsoft", "Windows Terminal", "Fragments")
	if fi, err := os.Stat(fragmentsBase); err != nil || !fi.IsDir() {
		return nil
	}

	fragDir := filepath.Join(fragmentsBase, "Forcefield")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		return err
	}

	iconDest := filepath.Join(fragDir, "logo.png")
	if logoPath != "" {
		if src, err := os.Open(logoPath); err == nil {
			defer src.Close()
			if dst, err := os.Create(iconDest); err == nil {
				defer dst.Close()
				_, _ = io.Copy(dst, src)
			}
		}
	}

	frag := wtFragment{
		Profiles: []wtProfile{
			{
				GUID:        forcefieldProfileGUID,
				Name:        "Forcefield",
				CommandLine: "ff",
				Icon:        iconDest,
			},
		},
	}

	data, err := json.MarshalIndent(frag, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(fragDir, "forcefield.json"), data, 0o644)
}
