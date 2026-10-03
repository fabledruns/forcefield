// Package subject resolves benchmark subjects: the subject contract that
// describes how to drive a harness, and the provenance of the executable
// under measurement.
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

	"forcefield/internal/hpov/bench"
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
// toolchain. Version comes from the contract's version command.
//
// The path is resolved to an absolute path first: benchmarks hand the
// child their own working directory, and a relative path would then be
// resolved against the fixture instead of the caller's directory.
func Probe(label, path string, contract bench.Contract, source string) (ProbeResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("subject %s: resolve %q: %w", label, path, err)
	}
	path = abs
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
		Label: label,
		// Identity comes from the subject's contract, never from a
		// product name assumed by the harness.
		Product: contract.Product,
		Binary: schema.Binary{
			Basename:  filepath.Base(path),
			SHA256:    fmt.Sprintf("%x", h.Sum(nil)),
			SizeBytes: size,
			Source:    source,
		},
	}
	// Build info of the *subject* binary via stdlib debug/buildinfo:
	// no toolchain required. A non-Go subject simply has none.
	if info, err := readBuildInfo(path); err == nil {
		sub.Binary.GoVersion = info.GoVersion
		sub.Binary.GoOS = info.GOOS
		sub.Binary.GoArch = info.GOARCH
		sub.GitCommit = info.Revision
		sub.GitDirty = info.Modified
	}
	if v, ok := versionLine(path, contract.Version); ok {
		sub.Version = v
	}
	return ProbeResult{Label: label, Path: path, Subject: sub}, nil
}

// versionLine runs the contract's version command briefly and returns
// the first stdout line. A contract with no version command records no
// version rather than guessing a flag.
func versionLine(path string, probe bench.Probe) (string, bool) {
	if !probe.Defined() {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := spawn.Run(ctx, spawn.Options{
		Path:          path,
		Args:          probe.Args,
		CaptureStdout: true,
		Timeout:       15 * time.Second,
	})
	if err != nil || res.ExitCode != probe.WantExit() {
		return "", false
	}
	line := strings.TrimSpace(strings.SplitN(res.Stdout, "\n", 2)[0])
	if line == "" {
		return "", false
	}
	return line, true
}
