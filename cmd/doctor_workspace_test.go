package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
)

// TestDoctorWorkspace_ReportsRootAndMode pins that doctor names the
// resolved root and the effective mode, and fails loudly on a bad root.
func TestDoctorWorkspace_ReportsRootAndMode(t *testing.T) {
	dir := t.TempDir()

	var lines []string
	report := func(v verdict, format string, args ...any) {
		lines = append(lines, v.String()+" "+fmt.Sprintf(format, args...))
	}

	strict := &config.Config{}
	strict.Workspace.Root = dir
	strict.Workspace.Mode = config.WorkspaceStrict
	doctorWorkspace(strict, report)
	joined := strings.Join(lines, "\n")
	// The runtime canonicalizes the explicit root (EvalSymlinks), so on
	// Windows the report may use the long spelling (...\runneradmin\...)
	// while t.TempDir() returns the short spelling (...RUNNER~1...).
	// Compare canonically and case-insensitively, like workspace_test.go.
	want := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		want = resolved
	}
	lower := strings.ToLower(joined)
	if (!strings.Contains(lower, strings.ToLower(want)) && !strings.Contains(lower, strings.ToLower(dir))) || !strings.Contains(joined, "strict") {
		t.Errorf("strict report = %q, want root and strict", joined)
	}

	lines = nil
	plain := &config.Config{}
	doctorWorkspace(plain, report)
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "permissive") {
		t.Errorf("default report = %q, want permissive", joined)
	}

	lines = nil
	bad := &config.Config{}
	bad.Workspace.Root = dir + "-missing"
	bad.Workspace.Mode = config.WorkspaceStrict
	doctorWorkspace(bad, report)
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "[FAIL]") {
		t.Errorf("bad-root report = %q, want [FAIL]", joined)
	}

	doctorWorkspace(nil, report) // must not panic
}
