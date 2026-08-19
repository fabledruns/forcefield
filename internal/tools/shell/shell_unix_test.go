//go:build !windows

package shell

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestBuildCommand_SelectsBashLoginShell(t *testing.T) {
	// Stub the lookup so the test proves the invocation shape (bash -lc)
	// regardless of what's installed.
	orig := bashLookPath
	defer func() { bashLookPath = orig }()

	var lookedUp string
	fakeBash := "/usr/bin/bash"
	bashLookPath = func(name string) (string, error) {
		lookedUp = name
		return fakeBash, nil
	}

	cmd, cleanup, err := buildCommand(context.Background(), "echo hi", "/tmp/proj", []string{"FF_TEST=X"})
	if err != nil {
		t.Fatalf("buildCommand() error = %v", err)
	}
	if lookedUp != "bash" {
		t.Errorf("looked up %q, want %q", lookedUp, "bash")
	}
	if cmd.Path != fakeBash {
		t.Errorf("cmd.Path = %q, want %q", cmd.Path, fakeBash)
	}
	wantArgs := []string{fakeBash, "-lc", "echo hi"}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("cmd.Args = %#v, want %#v", cmd.Args, wantArgs)
	}
	for i := range wantArgs {
		if cmd.Args[i] != wantArgs[i] {
			t.Errorf("cmd.Args[%d] = %q, want %q", i, cmd.Args[i], wantArgs[i])
		}
	}
	if cmd.Dir != "/tmp/proj" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/proj")
	}
	foundEnv := false
	for _, kv := range cmd.Env {
		if kv == "FF_TEST=X" {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Errorf("cmd.Env = %#v, want it to contain the extra FF_TEST=X pair", cmd.Env)
	}
	if cleanup != nil {
		t.Error("cleanup is non-nil on Unix, want nil")
	}
}

func TestBuildCommand_ResolvesBashFromRealPath(t *testing.T) {
	cmd, _, err := buildCommand(context.Background(), "true", "", nil)
	if err != nil {
		t.Skipf("bash not available on this machine's PATH: %v", err)
	}
	if !strings.HasSuffix(cmd.Path, "bash") && !strings.HasSuffix(cmd.Path, "bash.exe") {
		t.Errorf("resolved shell = %q, want bash", cmd.Path)
	}
}

func TestShell_BashMissingProducesClearError(t *testing.T) {
	orig := bashLookPath
	defer func() { bashLookPath = orig }()
	bashLookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}

	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("Execute() returned a Go error = %v, want a failed Result", err)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false, want true; content: %s", result.Content)
	}
	want := "bash was not found on PATH; Forcefield requires Bash for shell commands"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
	// The command must not have been run through any other shell.
	if strings.TrimSpace(result.Stdout) == "hello" {
		t.Errorf("Stdout = %q; command appears to have executed despite missing bash", result.Stdout)
	}
}
