//go:build windows

package shell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// stubWSLExe replaces the wsl.exe resolution seam for the test.
func stubWSLExe(t *testing.T, fn func() (string, error)) {
	t.Helper()
	orig := wslExePath
	t.Cleanup(func() { wslExePath = orig })
	wslExePath = fn
}

func TestBuildCommand_WSLInvocationShape(t *testing.T) {
	stubWSLExe(t, func() (string, error) { return `C:\Windows\System32\wsl.exe`, nil })

	cmd, cleanup, err := buildCommand(context.Background(), "echo hi", `C:\proj`, []string{"A=1", "B=two words"})
	if err != nil {
		t.Fatalf("buildCommand() error = %v", err)
	}
	want := []string{
		`C:\Windows\System32\wsl.exe`,
		"--cd", `C:\proj`,
		"--exec",
		"/usr/bin/env", "A=1", "B=two words",
		"/bin/bash", "-lc", "echo hi",
	}
	if len(cmd.Args) != len(want) {
		t.Fatalf("cmd.Args = %#v, want %#v", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("cmd.Args[%d] = %q, want %q", i, cmd.Args[i], want[i])
		}
	}
	// The host process must never chdir or set a Linux environment for the
	// wsl.exe child directly; --cd and the env argv handle both inside WSL.
	if cmd.Dir != "" {
		t.Errorf("cmd.Dir = %q, want empty (working directory is handled by --cd)", cmd.Dir)
	}
	if cmd.Env == nil {
		t.Errorf("cmd.Env = nil, want the inherited Windows environment for wsl.exe")
	}
	if cleanup != nil {
		t.Error("cleanup is non-nil for a small payload, want nil")
	}
}

func TestBuildCommand_DistroOverrideAndOmissions(t *testing.T) {
	stubWSLExe(t, func() (string, error) { return `C:\Windows\System32\wsl.exe`, nil })
	t.Setenv("FORCEFIELD_WSL_DISTRO", "Ubuntu")

	// No env pairs -> no /usr/bin/env hop; no cwd -> no --cd; the distro
	// override comes first.
	cmd, _, err := buildCommand(context.Background(), "true", "", nil)
	if err != nil {
		t.Fatalf("buildCommand() error = %v", err)
	}
	want := []string{`C:\Windows\System32\wsl.exe`, "--distribution", "Ubuntu", "--exec", "/bin/bash", "-lc", "true"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("cmd.Args = %#v, want %#v", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("cmd.Args[%d] = %q, want %q", i, cmd.Args[i], want[i])
		}
	}
}

func TestBuildCommand_MissingWSLIsClearError(t *testing.T) {
	stubWSLExe(t, func() (string, error) { return "", errors.New("not found") })

	_, _, err := buildCommand(context.Background(), "echo hi", "", nil)
	if err == nil {
		t.Fatal("buildCommand() error = nil, want an error when wsl.exe is missing")
	}
	if !strings.Contains(err.Error(), "wsl.exe") {
		t.Errorf("error = %q, want it to mention wsl.exe", err)
	}
}

func TestBuildCommand_LargePayloadSpillsToScriptFile(t *testing.T) {
	stubWSLExe(t, func() (string, error) { return `C:\Windows\System32\wsl.exe`, nil })

	command := "echo " + strings.Repeat("x", 40000)
	cmd, cleanup, err := buildCommand(context.Background(), command, `C:\proj`, []string{"A=has spaces"})
	if err != nil {
		t.Fatalf("buildCommand() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want a staged script cleanup for a large payload")
	}
	defer cleanup()

	// The spill runs `bash -l <script>`; the command itself must be off the
	// command line and inside the staged script instead.
	if len(cmd.Args) < 3 {
		t.Fatalf("cmd.Args = %#v, too short", cmd.Args)
	}
	bashAt := -1
	for i, a := range cmd.Args {
		if a == "/bin/bash" {
			bashAt = i
		}
	}
	if bashAt < 0 || cmd.Args[bashAt+1] != "-l" {
		t.Fatalf("cmd.Args = %#v, want /bin/bash -l <script> for a spilled payload", cmd.Args)
	}
	scriptWslPath := cmd.Args[bashAt+2]
	if strings.Contains(scriptWslPath, strings.Repeat("x", 100)) {
		t.Errorf("script path %q appears to contain the command itself", scriptWslPath)
	}
	if !strings.HasPrefix(scriptWslPath, "/mnt/") {
		t.Errorf("script path %q, want a /mnt/... drvfs path", scriptWslPath)
	}

	// Map the WSL path back to its Windows form and read the staged script
	// to prove it carries the env exports and the command verbatim.
	// "/mnt/c/Users/..." -> "C:\Users\...".
	hostPath := strings.ToUpper(string(scriptWslPath[5])) + `:\` + strings.ReplaceAll(scriptWslPath[7:], `/`, `\`)
	data, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("staged script not readable from Windows at %q: %v", hostPath, err)
	}
	if !strings.Contains(string(data), "export A='has spaces'\n") {
		t.Errorf("staged script missing quoted env export:\n%.200s", data)
	}
	if !strings.Contains(string(data), command) {
		t.Errorf("staged script does not contain the command verbatim")
	}

	cleanup()
	if _, err := os.Stat(hostPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staged script still exists after cleanup: %v", err)
	}
}

func TestWSLCdPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{``, ``},
		{`C:\Users\Admin\project`, `C:\Users\Admin\project`},
		// Forward-slash drive paths are normalized by filepath.Abs; wsl.exe
		// accepts either form.
		{`C:/Users/Admin/project`, `C:\Users\Admin\project`},
		{`/home/user/project`, `/home/user/project`},
		{`\\wsl$\Ubuntu\home\user`, `\\wsl$\Ubuntu\home\user`},
	}
	for _, tc := range cases {
		if got := wslCdPath(tc.in); got != tc.want {
			t.Errorf("wslCdPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Relative paths must be absolutized: --cd requires absolute paths.
	if got := wslCdPath(`some\rel`); !filepath.IsAbs(got) {
		t.Errorf("wslCdPath(`some\\rel`) = %q, want an absolute path", got)
	}
}

func TestWSLPathFromWindows(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`C:\Users\a\f.sh`, `/mnt/c/Users/a/f.sh`},
		{`d:\Temp\x y\z.sh`, `/mnt/d/Temp/x y/z.sh`},
		{`\\server\share\f.sh`, `\\server\share\f.sh`}, // no drive letter: unchanged
		{`/posix/path`, `/posix/path`},
	}
	for _, tc := range cases {
		if got := wslPathFromWindows(tc.in); got != tc.want {
			t.Errorf("wslPathFromWindows(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidWorkingDir_Windows(t *testing.T) {
	if !validWorkingDir(`C:\Windows`) {
		t.Error("validWorkingDir(C:\\Windows) = false, want true")
	}
	if validWorkingDir(`C:\Windows\does-not-exist-hopefully`) {
		t.Error("validWorkingDir(nonexistent) = true, want false")
	}
	// POSIX paths can't be stat'ed from Windows. With wsl.exe unresolvable
	// the check must defer (report true) so the backend probe surfaces the
	// real problem instead of a bogus "directory does not exist".
	stubWSLExe(t, func() (string, error) { return "", errors.New("no wsl") })
	if !validWorkingDir(`/home/user`) {
		t.Error("validWorkingDir(/home/user) = false with WSL missing, want true (deferred to probe)")
	}
}

func TestDecodeWSLText(t *testing.T) {
	msg := "wsl: An error occurred mounting the distribution disk"
	encoded := make([]byte, 0, len(msg)*2)
	for _, u := range utf16.Encode([]rune(msg)) {
		encoded = append(encoded, byte(u), byte(u>>8))
	}

	if got := decodeWSLText(encoded); got != msg {
		t.Errorf("decodeWSLText(UTF-16LE) = %q, want %q", got, msg)
	}
	if got := decodeWSLText([]byte("plain utf-8 stderr")); got != "plain utf-8 stderr" {
		t.Errorf("decodeWSLText(UTF-8) = %q, want passthrough", got)
	}
	binary := []byte{0x00, 0xff, 0x01, 0xfe, 0x02, 0xfd, 0x03, 0xfc}
	if got := decodeWSLText(binary); got != string(binary) {
		t.Errorf("decodeWSLText(binary) = %q, want raw passthrough", got)
	}
}

func TestBackendFailure(t *testing.T) {
	mountErr := "wsl: An error occurred mounting the distribution disk, it was mounted read-only as a fallback."
	chdirErr := "<3>WSL (326 - Relay) ERROR: CreateProcessCommon:792: chdir(/nope) failed 2"

	cases := []struct {
		name   string
		stderr string
		stdout string
		want   bool
	}{
		{"mount failure", mountErr, "", true},
		{"chdir failure", chdirErr, "", true},
		{"command's own error", "grep: no such file", "", false},
		{"chdir-like text without WSL marker", "python: chdir(/x) failed", "", false},
		{"command ran and printed output", mountErr, "partial output", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail, hint := backendFailure(tc.stderr, tc.stdout)
			if tc.want && hint == "" {
				t.Errorf("backendFailure(%q) returned no hint, want one", tc.stderr)
			}
			if !tc.want && hint != "" {
				t.Errorf("backendFailure(%q) = hint %q, want none", tc.stderr, hint)
			}
			if tc.want && !strings.Contains(detail, "wsl") && !strings.Contains(detail, "WSL") {
				t.Errorf("backendFailure detail = %q, want the decoded diagnostic", detail)
			}
		})
	}
}

func TestShell_WSLUnavailableIsStructuredError(t *testing.T) {
	stubWSLExe(t, func() (string, error) { return "", errors.New("not found") })

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
	if !strings.Contains(result.Content, "wsl.exe") {
		t.Errorf("Content = %q, want it to explain that wsl.exe was not found", result.Content)
	}
	if result.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 (the command never ran)", result.ExitCode)
	}
	if strings.TrimSpace(result.Stdout) == "hello" {
		t.Errorf("Stdout = %q; command appears to have executed despite missing WSL", result.Stdout)
	}

	// A second execution must reuse the cached probe outcome even if WSL
	// appears meanwhile: one clear diagnosis per Shell, no flapping.
	stubWSLExe(t, func() (string, error) { return `C:\Windows\System32\wsl.exe`, nil })
	again, err := s.Execute(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !again.IsError || !strings.Contains(again.Content, "wsl.exe") {
		t.Errorf("second run Content = %q, want the cached WSL-missing error", again.Content)
	}
}

func TestShell_EnvSurvivesWSLBoundary(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": `printf 'A=[%s] B=[%s]' "$FF_WSL_A" "$FF_WSL_B"`,
		"env": map[string]any{
			"FF_WSL_A": "simple",
			"FF_WSL_B": "with spaces 'quotes' and $shell chars",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true, content: %s", result.Content)
	}
	want := "A=[simple] B=[with spaces 'quotes' and $shell chars]"
	if !strings.Contains(result.Stdout, want) {
		t.Errorf("Stdout = %q, want it to contain %q (env values must cross the WSL boundary verbatim)", result.Stdout, want)
	}
}

func TestShell_LargeCommandSpillToScriptRuns(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	// A command far over the ~32K command-line limit forces the staged
	// script path; it must still execute as ordinary Bash.
	command := "for i in 1 2 3; do echo line-$i-" + strings.Repeat("p", 12000) + "; done"
	result, err := s.Execute(context.Background(), map[string]any{
		"command":         command,
		"timeout_seconds": 30,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true, content: %.200s", result.Content)
	}
	if !strings.Contains(result.Stdout, "line-1-") || !strings.Contains(result.Stdout, "line-3-") {
		t.Errorf("Stdout does not contain the expected loop output (%d bytes)", len(result.Stdout))
	}
}

func TestShell_PosixCwdValidatedInsideDistro(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": "pwd",
		"cwd":     "/definitely-not-a-real-directory-xyz",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Fatal("result.IsError = false, want true for a nonexistent POSIX cwd")
	}
	if !strings.Contains(result.Content, "working directory") {
		t.Errorf("Content = %q, want a working-directory message (not a silent fallback to /)", result.Content)
	}
}
