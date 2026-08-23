//go:build windows

package sandbox

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

// TestNativeRelayInvocationShape pins native-mode Windows behavior to the
// historical wsl.exe relay so existing users keep byte-identical
// invocations after the executor refactor.
func TestNativeRelayInvocationShape(t *testing.T) {
	stubWSLExe(t, func() (string, error) { return `C:\Windows\System32\wsl.exe`, nil })

	cmd, cleanup, err := buildNativeRelay(context.Background(), "echo hi", `C:\proj`, []string{"A=1", "B=two words"})
	if err != nil {
		t.Fatalf("buildNativeRelay() error = %v", err)
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

func TestNativeRelayDistroOverride(t *testing.T) {
	stubWSLExe(t, func() (string, error) { return `C:\Windows\System32\wsl.exe`, nil })
	t.Setenv("FORCEFIELD_WSL_DISTRO", "Ubuntu")

	cmd, _, err := buildNativeRelay(context.Background(), "true", "", nil)
	if err != nil {
		t.Fatalf("buildNativeRelay() error = %v", err)
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

func TestNativeRelayMissingWSLIsClearError(t *testing.T) {
	stubWSLExe(t, func() (string, error) { return "", errors.New("not found") })

	_, _, err := buildNativeRelay(context.Background(), "echo hi", "", nil)
	if err == nil || !strings.Contains(err.Error(), "wsl.exe") {
		t.Fatalf("error = %v, want a clear wsl.exe message", err)
	}
}

func TestWSLCdPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{``, ``},
		{`C:\Users\Admin\project`, `C:\Users\Admin\project`},
		{`C:/Users/Admin/project`, `C:\Users\Admin\project`},
		{`/home/user/project`, `/home/user/project`},
		{`\\wsl$\Ubuntu\home\user`, `\\wsl$\Ubuntu\home\user`},
	}
	for _, tc := range cases {
		if got := wslCdPath(tc.in); got != tc.want {
			t.Errorf("wslCdPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := wslCdPath(`some\rel`); !filepath.IsAbs(got) {
		t.Errorf("wslCdPath(`some\\rel`) = %q, want an absolute path", got)
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

func TestBackendFailureSignatures(t *testing.T) {
	mountErr := "wsl: An error occurred mounting the distribution disk, it was mounted read-only as a fallback."
	chdirErr := "<3>WSL (326 - Relay) ERROR: CreateProcessCommon:792: chdir(/nope) failed 2"

	cases := []struct {
		name, stderr, stdout string
		want                 bool
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

// healthyRestricted returns a wslExecutor with both probes pre-answered so
// Prepare can be exercised without spawning WSL.
func healthyRestricted(t *testing.T, ws string, netSupported bool) *wslExecutor {
	t.Helper()
	e, err := newWSLExecutor(Policy{Mode: ModeWSL, Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	e.healthy = true
	e.netProbe = &netProbeResult{}
	if netSupported {
		e.netProbe.supported = true
		e.netProbe.unshare = "/usr/bin/unshare"
	} else {
		e.netProbe.err = errors.New("unavailable")
	}
	return e
}

// TestRestrictedSeversHostEnvironment is the API-key guarantee: whatever
// the Forcefield process environment holds, only the launcher allowlist
// plus an empty WSLENV may reach the distribution.
func TestRestrictedSeversHostEnvironment(t *testing.T) {
	t.Setenv("NVIDIA_API_KEY", "nvapi-must-not-leak")
	t.Setenv("FF_CANARY", "canary")

	dir := t.TempDir()
	e := healthyRestricted(t, dir, true)

	prepared, err := e.Prepare(context.Background(), Request{Command: "env", ExtraEnv: []string{"FF_EXTRA=1"}})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	env := prepared.Cmd.Env
	if len(env) > len(restrictedHostEnvNames)+1+len([]string{"FF_EXTRA=1"})+1 {
		// allowlist + WSLENV + extras; anything more means host vars leaked.
		t.Errorf("restricted environment too large (%d entries): %#v", len(env), env)
	}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "SystemRoot", "TEMP", "TMP", "WSLENV", "PATH":
			// PATH could legitimately appear only if we ever forward it; today
			// it must not come from the host environment.
			if name == "PATH" {
				t.Errorf("host PATH leaked into restricted environment")
			}
		default:
			if strings.Contains(kv, "NVIDIA_API_KEY") || strings.Contains(kv, "must-not-leak") || strings.Contains(kv, "canary") {
				t.Errorf("secret or host variable leaked: %q", kv)
			}
		}
	}
	var wslenv string
	for _, kv := range env {
		if strings.HasPrefix(kv, "WSLENV=") {
			wslenv = kv
		}
	}
	if wslenv != "WSLENV=" {
		t.Errorf("WSLENV = %q, want explicitly empty to sever host variable sharing", wslenv)
	}
}

// TestRestrictedFailsClosedWithoutNetworkIsolation pins the fail-closed
// rule: a requested network deny that the distribution cannot deliver
// refuses to run rather than silently executing with host networking.
func TestRestrictedFailsClosedWithoutNetworkIsolation(t *testing.T) {
	dir := t.TempDir()
	e := healthyRestricted(t, dir, false)

	prepared, err := e.Prepare(context.Background(), Request{Command: "curl example.com"})
	if err == nil {
		t.Fatal("Prepare() succeeded without achievable network isolation; this would be silent fallback")
	}
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("error = %v, want ErrBackendUnavailable", err)
	}
	if prepared != nil {
		t.Fatalf("prepared command = %+v on failure", prepared)
	}
	if !strings.Contains(err.Error(), "network") {
		t.Errorf("error should explain the network problem: %v", err)
	}
}

// TestRestrictedPrefixesNetworkNamespace checks that an enforced network
// deny actually wraps the Linux argv in unshare.
func TestRestrictedPrefixesNetworkNamespace(t *testing.T) {
	dir := t.TempDir()
	e := healthyRestricted(t, dir, true)

	prepared, err := e.Prepare(context.Background(), Request{Command: "go test ./...", ExtraEnv: []string{"GOFLAGS=-count=1"}})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	args := prepared.Cmd.Args
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/usr/bin/unshare --user --net --map-root-user /usr/bin/env GOFLAGS=-count=1 /bin/bash -lc go test ./...") {
		t.Errorf("argv missing namespace prefix + env + bash chain:\n%s", joined)
	}
	// The launcher must still receive only the restricted environment.
	for _, kv := range prepared.Cmd.Env {
		if strings.Contains(kv, "=") && !containsAny(kv, "SystemRoot", "TEMP=", "TMP=", "WSLENV=") {
			t.Errorf("unexpected launcher env entry %q", kv)
		}
	}
}

// TestRestrictedRejectsWorkspaceEscape proves scope enforcement happens
// before any process construction.
func TestRestrictedRejectsWorkspaceEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()

	e, err := newWSLExecutor(Policy{Mode: ModeWSL, Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	// Mark probes as answered so the only possible failure is the policy one.
	e.healthy = true
	e.netProbe = &netProbeResult{supported: true, unshare: "/usr/bin/unshare"}

	_, err = e.Prepare(context.Background(), Request{Command: "pwd", Dir: outside})
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("error = %v, want ErrWorkspaceEscape", err)
	}

	// Inside the workspace resolves and lands translated under --cd.
	in := filepath.Join(ws, "sub")
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	prepared, err := e.Prepare(context.Background(), Request{Command: "pwd", Dir: "sub"})
	if err != nil {
		t.Fatalf("Prepare(sub) error = %v", err)
	}
	if !strings.Contains(strings.Join(prepared.Cmd.Args, " "), "--cd") {
		t.Error("expected --cd pinning for the resolved directory")
	}
}

func TestRestrictedDescribeHonesty(t *testing.T) {
	dir := t.TempDir()

	unforced := healthyRestricted(t, dir, false)
	d := unforced.Describe(context.Background())
	if d.FilesystemConfined {
		t.Error("Describe claims filesystem confinement plain wsl.exe cannot provide")
	}
	if d.NetworkEnforced {
		t.Error("Describe claims network enforcement while the probe failed")
	}
	lines := strings.Join(d.SummaryLines(), "\n")
	if !strings.Contains(lines, "NOT enforced") || !strings.Contains(lines, "NOT blocked") {
		t.Errorf("Describe must state its limits plainly:\n%s", lines)
	}

	enforced := healthyRestricted(t, dir, true)
	d = enforced.Describe(context.Background())
	if !d.NetworkEnforced {
		t.Error("Describe denies enforcement although the probe supports it")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
