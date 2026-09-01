package shell

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

func TestShell_InvalidTimeoutIsArgumentError(t *testing.T) {
	s := NewShell()
	for _, raw := range []any{"thirty", 0, -5, true} {
		_, err := s.Execute(context.Background(), map[string]any{
			"command":         "echo hi",
			"timeout_seconds": raw,
		})
		argErr, ok := err.(*tools.ArgumentError)
		if !ok {
			t.Fatalf("timeout_seconds=%v: error = %v, want ArgumentError", raw, err)
		}
		if argErr.Field != "timeout_seconds" {
			t.Errorf("ArgumentError.Field = %q, want timeout_seconds", argErr.Field)
		}
	}
}

func TestShell_NonexistentCwdIsResultError(t *testing.T) {
	// The cwd check runs inside the executor, after the backend probe, so
	// a usable Bash backend is required for the working-directory error to
	// be reachable. Runners without WSL (GitHub windows-latest) skip:
	// they'd otherwise hit the backend-unavailable message instead.
	requireShellBackend(t)
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": "echo hi",
		"cwd":     t.TempDir() + "/does-not-exist",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (failure should be a Result)", err)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false, content: %s", result.Content)
	}
	if !strings.Contains(result.Content, "working directory") {
		t.Errorf("Content = %q, want a working-directory message", result.Content)
	}
}

func TestShell_InvalidEnvIsArgumentError(t *testing.T) {
	s := NewShell()

	_, err := s.Execute(context.Background(), map[string]any{
		"command": "echo hi",
		"env":     "not-an-object",
	})
	if argErr, ok := err.(*tools.ArgumentError); !ok {
		t.Fatalf("env=string: error = %v, want ArgumentError", err)
	} else if argErr.Field != "env" {
		t.Errorf("ArgumentError.Field = %q, want env", argErr.Field)
	}

	_, err = s.Execute(context.Background(), map[string]any{
		"command": "echo hi",
		"env":     map[string]any{"FOO": 42},
	})
	if _, ok := err.(*tools.ArgumentError); !ok {
		t.Fatalf("env with non-string value: error = %v, want ArgumentError", err)
	}
}

func TestShell_StderrIncludedInContentOnSuccess(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	command := commandChain("echo out", "echo err "+stderrRedirect())
	result, err := s.Execute(context.Background(), map[string]any{"command": command})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true, content: %s", result.Content)
	}
	if !strings.Contains(result.Stdout, "out") {
		t.Errorf("Stdout = %q, want it to contain %q", result.Stdout, "out")
	}
	if !strings.Contains(result.Stderr, "err") {
		t.Errorf("Stderr = %q, want it to contain %q", result.Stderr, "err")
	}
	if !strings.Contains(result.Content, "err") {
		t.Errorf("Content = %q, want stderr diagnostics included on success", result.Content)
	}
}

func TestShell_CapturesVeryLongLine(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	// A single line well past the old 1 MiB scanner cap must be captured
	// whole, not truncated with a "token too long" note. The line is
	// generated inside Bash (rather than passed through the environment)
	// so the test also works under WSL, where wsl.exe's command line
	// cannot carry multi-megabyte values.
	// Note: shell caps at 2 MiB, so a 3 MiB line should be truncated with marker.
	size := 3 << 20
	command := "head -c " + strconv.Itoa(size) + " /dev/zero | tr '\\0' x"
	result, err := s.Execute(context.Background(), map[string]any{
		"command":         command,
		"timeout_seconds": 60,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true, content: %.100s", result.Content)
	}
	// With 2 MiB cap, 3 MiB should be truncated
	if got := len(strings.TrimRight(result.Stdout, "\n")); got > maxShellOutputBytes {
		t.Errorf("captured stdout length = %d, exceeds cap %d", got, maxShellOutputBytes)
	}
	if got := len(strings.TrimRight(result.Stdout, "\n")); got < maxShellOutputBytes-1024 {
		t.Logf("result stdout len %d, content len %d, stderr len %d, content snippet %.500q", len(result.Stdout), len(result.Content), len(result.Stderr), result.Content)
		t.Errorf("captured stdout length = %d, want near cap %d (truncated)", got, maxShellOutputBytes)
	}
	if !strings.Contains(result.Content, "truncated") {
		t.Errorf("expected truncation marker, got content snippet %.500q", result.Content)
	}
}

func TestShell_EnvVarsApplied(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	echo := "echo $FORCEFIELD_TEST_VAR"
	result, err := s.Execute(context.Background(), map[string]any{
		"command": echo,
		"env":     map[string]any{"FORCEFIELD_TEST_VAR": "applied"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "applied") {
		t.Errorf("Stdout = %q, want env var applied", result.Stdout)
	}
}

func TestShell_FinishesWithErrorFieldsPopulated(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{"command": commandChain("echo before", "exit 9")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false")
	}
	if result.ExitCode != 9 {
		t.Errorf("ExitCode = %d, want 9", result.ExitCode)
	}
	if !strings.Contains(result.Stdout, "before") {
		t.Errorf("Stdout = %q, want pre-failure output kept", result.Stdout)
	}
	if result.DurationMs == 0 && result.Command == "" {
		t.Errorf("DurationMs/Command not populated: %+v", result)
	}
}

func TestDetectInteractiveCommand_WrappersAndQuotes(t *testing.T) {
	cases := []struct {
		command string
		want    string
	}{
		{"sudo vim", "vim"},
		{"nohup top", "top"},
		{"env python", "python"},
		{`"C:\Program Files\vim.exe" file.txt`, "vim"},
		{"echo hi && command ssh host", "ssh"},
		{"echo hi", ""},
		{"python3 -c 'print(1)'", ""},
	}
	for _, tc := range cases {
		got, ok := detectInteractiveCommand(tc.command)
		if tc.want == "" && ok {
			t.Errorf("detectInteractiveCommand(%q) = %q, want none", tc.command, got)
		}
		if tc.want != "" && (!ok || got != tc.want) {
			t.Errorf("detectInteractiveCommand(%q) = %q, %v; want %q", tc.command, got, ok, tc.want)
		}
	}
}
