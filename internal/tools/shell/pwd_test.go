package shell

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/sandbox"
)

func TestPWD_DefaultUnconfiguredReturnsCanonicalCwd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() unexpected error: %v", err)
	}
	want, err := sandbox.ResolveWithinWorkspace("", "")
	if err != nil {
		t.Fatalf("ResolveWithinWorkspace error: %v", err)
	}

	p := NewPWD()
	res, err := p.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute() result.IsError = true, content: %s", res.Content)
	}
	if res.Content != want {
		t.Fatalf("Execute() content = %q, want %q (raw wd: %q)", res.Content, want, wd)
	}
}

func TestPWD_ConfiguredWorkspace(t *testing.T) {
	dir := t.TempDir()
	want, err := sandbox.ResolveWithinWorkspace(dir, "")
	if err != nil {
		t.Fatalf("ResolveWithinWorkspace error: %v", err)
	}

	p := NewPWDWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := p.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if res.Content != want {
		t.Fatalf("content = %q, want %q", res.Content, want)
	}
}

func TestPWD_BenchmarkTemporaryWorkspace(t *testing.T) {
	// Simulate an isolated temporary benchmark workspace.
	benchDir := t.TempDir()
	hostCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd error: %v", err)
	}

	p := NewPWDWithPolicy(sandbox.Policy{Workspace: benchDir})
	res, err := p.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}

	// Must match the isolated benchmark workspace, NOT the host process working directory.
	canonicalBench, err := sandbox.ResolveWithinWorkspace(benchDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != canonicalBench {
		t.Fatalf("content = %q, want %q", res.Content, canonicalBench)
	}
	if res.Content == hostCwd {
		t.Fatalf("content leaked host process working directory %q", hostCwd)
	}
}

func TestPWD_PathCanonicalization(t *testing.T) {
	baseDir := t.TempDir()
	subDir := filepath.Join(baseDir, "nested", "workspace")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Supply a path requiring evaluation of relative components (. and ..)
	uncleanPath := filepath.Join(baseDir, "nested", "other", "..", "workspace")
	canonicalExpected, err := sandbox.ResolveWithinWorkspace(subDir, "")
	if err != nil {
		t.Fatal(err)
	}

	p := NewPWDWithPolicy(sandbox.Policy{Workspace: uncleanPath})
	res, err := p.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if res.Content != canonicalExpected {
		t.Fatalf("content = %q, want canonical %q", res.Content, canonicalExpected)
	}
}

func TestPWD_InvalidWorkspaceFailsGracefully(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "does_not_exist_xyz")
	p := NewPWDWithPolicy(sandbox.Policy{Workspace: nonExistent})
	res, err := p.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute should not return Go error for expected path failures: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected res.IsError = true for non-existent workspace root")
	}
}

func TestPWD_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := NewPWDWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := p.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected res.IsError = true when context is cancelled")
	}
}
