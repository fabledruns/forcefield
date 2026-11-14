package shell

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"path/filepath"

	"forcefield/internal/sandbox"
)

func TestShell_InputSchemaExplainsCwdExpectations(t *testing.T) {
	s := NewShell()
	toolDesc := s.Description()
	for _, want := range []string{
		"permissive native mode",
		"process working directory",
		"confined mode",
		"workspace root",
	} {
		if !strings.Contains(toolDesc, want) {
			t.Errorf("shell description missing %q: %q", want, toolDesc)
		}
	}

	props := s.InputSchema()["properties"].(map[string]any)
	cwdDesc := props["cwd"].(map[string]any)["description"].(string)
	for _, want := range []string{
		"permissive native mode",
		"omitting cwd uses the process working directory",
		"resolve relative to the process working directory",
		"Workspace root host path",
		"confined mode",
		"workspace.mode: strict",
		"sandbox.mode: wsl",
		"sandbox.mode: isolated",
		`"."`,
		"Windows host path",
		"/home/agent/repo",
		`"root"`,
	} {
		if !strings.Contains(cwdDesc, want) {
			t.Errorf("shell.cwd description missing %q: %q", want, cwdDesc)
		}
	}
	if strings.Contains(cwdDesc, "current project directory") {
		t.Errorf("shell.cwd description should not claim cwd defaults to current project directory: %q", cwdDesc)
	}

	job := NewShellJob()
	jobProps := job.InputSchema()["properties"].(map[string]any)
	jobCwdDesc := jobProps["cwd"].(map[string]any)["description"].(string)
	for _, want := range []string{
		"permissive native mode",
		"process working directory",
		"confined mode",
		"Windows host path",
		"/home/agent/repo",
		`"root"`,
	} {
		if !strings.Contains(jobCwdDesc, want) {
			t.Errorf("shell_job.cwd description missing %q: %q", want, jobCwdDesc)
		}
	}
	if strings.Contains(jobCwdDesc, "Defaults to the workspace.") {
		t.Errorf("shell_job.cwd description should not claim it unconditionally defaults to workspace: %q", jobCwdDesc)
	}
}

func TestShell_ActionableErrorsForInvalidAndEscapedCwd(t *testing.T) {
	ws := `C:\Users\Admin\Documents\projects\forcefield`

	t.Run("permissive invalid linux-style cwd", func(t *testing.T) {
		execErr := fmt.Errorf(
			"%w: /home/agent/repo is a Linux-style path and not a valid Windows host directory (workspace root is %s; omit cwd or pass a Windows host path)",
			sandbox.ErrInvalidDir, ws,
		)
		s := NewShellWithExecutor(&failingExecutor{prepareErr: execErr})
		res, err := s.Execute(context.Background(), map[string]any{
			"command": "echo hi",
			"cwd":     "/home/agent/repo",
		})
		if err != nil {
			t.Fatalf("Execute error = %v", err)
		}
		if !res.IsError {
			t.Fatal("expected IsError = true")
		}
		for _, want := range []string{"working directory", "Linux-style", ws, "omit cwd"} {
			if !strings.Contains(res.Content, want) {
				t.Errorf("Content = %q, want %q", res.Content, want)
			}
		}
	})

	t.Run("strict escaped linux-style cwd", func(t *testing.T) {
		execErr := fmt.Errorf(
			"%w: /home/agent/repo is a Linux filesystem path; the sandboxed workspace is %s (omit path/cwd or pass \".\" for the workspace root)",
			sandbox.ErrWorkspaceEscape, ws,
		)
		s := NewShellWithExecutor(&failingExecutor{prepareErr: execErr})
		res, err := s.Execute(context.Background(), map[string]any{
			"command": "echo hi",
			"cwd":     "/home/agent/repo",
		})
		if err != nil {
			t.Fatalf("Execute error = %v", err)
		}
		if !res.IsError {
			t.Fatal("expected IsError = true")
		}
		for _, want := range []string{"outside the allowed workspace", "Linux filesystem path", ws, "omit cwd"} {
			if !strings.Contains(res.Content, want) {
				t.Errorf("Content = %q, want %q", res.Content, want)
			}
		}
	})

	t.Run("plain ErrInvalidDir still includes recovery hint", func(t *testing.T) {
		s := NewShellWithExecutor(&failingExecutor{prepareErr: fmt.Errorf("%w: C:\\missing", sandbox.ErrInvalidDir)})
		res, err := s.Execute(context.Background(), map[string]any{
			"command": "echo hi",
			"cwd":     `C:\missing`,
		})
		if err != nil {
			t.Fatalf("Execute error = %v", err)
		}
		if !res.IsError || !strings.Contains(res.Content, "working directory does not exist") || !strings.Contains(res.Content, "omit cwd") {
			t.Fatalf("unexpected result: %+v", res)
		}
	})
}

func TestShell_PermissiveVsConfinedCwdSemantics(t *testing.T) {
	ws := t.TempDir()
	canonicalWS, err := filepath.EvalSymlinks(ws)
	if err != nil {
		canonicalWS = ws
	}
	procCwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	canonicalCwd, err := filepath.EvalSymlinks(procCwd)
	if err != nil {
		canonicalCwd = procCwd
	}

	permExec, err := sandbox.NewExecutor(sandbox.Policy{Mode: sandbox.ModeNative, Workspace: ws, Strict: false})
	if err != nil {
		t.Fatalf("NewExecutor(permissive): %v", err)
	}
	for _, dir := range []string{"", "."} {
		prep, err := permExec.Prepare(context.Background(), sandbox.Request{Command: "echo ok", Dir: dir})
		if err != nil {
			t.Fatalf("permissive Prepare(%q): %v", dir, err)
		}
		if prep.Dir != canonicalCwd {
			t.Errorf("permissive Prepare(%q).Dir = %q, want process cwd %q", dir, prep.Dir, canonicalCwd)
		}
	}

	strictExec, err := sandbox.NewExecutor(sandbox.Policy{Mode: sandbox.ModeNative, Workspace: ws, Strict: true})
	if err != nil {
		t.Fatalf("NewExecutor(strict): %v", err)
	}
	for _, dir := range []string{"", "."} {
		prep, err := strictExec.Prepare(context.Background(), sandbox.Request{Command: "echo ok", Dir: dir})
		if err != nil {
			t.Fatalf("strict Prepare(%q): %v", dir, err)
		}
		if prep.Dir != canonicalWS {
			t.Errorf("strict Prepare(%q).Dir = %q, want workspace %q", dir, prep.Dir, canonicalWS)
		}
	}

	permShell := NewShellWithExecutor(permExec)
	res, err := permShell.Execute(context.Background(), map[string]any{
		"command": "echo hi",
		"cwd":     filepath.Join(ws, "missing-subdir"),
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "process working directory") || !strings.Contains(res.Content, ws) {
		t.Fatalf("unexpected permissive missing-dir result: %+v", res)
	}
}
