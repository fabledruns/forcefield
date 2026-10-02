// Package subject probes measured binaries for provenance: sha256,
// size, Go build info, and the --version string.
package subject

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/spawn"
)

// ProbeResult pairs the runner label/path with provenanced metadata.
type ProbeResult struct {
	Label   string
	Path    string
	Subject schema.Subject
}

// Probe stats path, hashes it, and reads debug/buildinfo without a
// toolchain. Version comes from a short `path --version` run.
func Probe(label, path, source string) (ProbeResult, error) {
	if _, err := os.Stat(path); err != nil {
		return ProbeResult{}, fmt.Errorf("subject %s: stat: %w", label, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("subject %s: open: %w", label, err)
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	_ = f.Close()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("subject %s: hash: %w", label, err)
	}
	sub := schema.Subject{
		Label:   label,
		Product: "forcefield",
		Binary: schema.Binary{
			Basename:  filepath.Base(path),
			SHA256:    fmt.Sprintf("%x", h.Sum(nil)),
			SizeBytes: size,
			Source:    source,
		},
	}
	// Build info of the *subject* binary via stdlib debug/buildinfo:
	// no toolchain required.
	if info, err := readBuildInfo(path); err == nil {
		sub.Binary.GoVersion = info.GoVersion
		sub.Binary.GoOS = info.GOOS
		sub.Binary.GoArch = info.GOARCH
		sub.GitCommit = info.Revision
		sub.GitDirty = info.Modified
	}
	if v, ok := versionLine(path); ok {
		sub.Version = v
	}
	return ProbeResult{Label: label, Path: path, Subject: sub}, nil
}

// versionLine runs `path --version` briefly and returns the first line.
func versionLine(path string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := spawn.Run(ctx, spawn.Options{
		Path:          path,
		Args:          []string{"--version"},
		CaptureStdout: true,
		Timeout:       15 * time.Second,
	})
	if err != nil || res.ExitCode != 0 {
		return "", false
	}
	line := strings.TrimSpace(strings.SplitN(res.Stdout, "\n", 2)[0])
	if line == "" {
		return "", false
	}
	return line, true
}
