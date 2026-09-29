package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"forcefield/internal/redact"
)

// Deterministic Host tests. Unit tests cover pure construction (env,
// resolution, validation) with no processes. Integration tests spawn the
// TestMCPHelperProcess child (the test binary re-executed, never a
// shell) and synchronize on protocol frames, pipe closure, and bounded
// polls — never on fixed sleeps, except two documented absence checks
// (no-respawn stability, descendant quiet) where waiting is the assertion.

// helperExe returns the test binary path for re-execution.
func helperExe(t *testing.T) string {
	t.Helper()
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("helper exe: %v", err)
	}
	return exe
}

// helperServerConfig builds a ServerConfig launching the fake MCP helper
// with literal extra environment. Args after the -test.run filter prove
// argv separation.
func helperServerConfig(t *testing.T, extraEnv map[string]string, extraArgs ...string) ServerConfig {
	t.Helper()
	env := map[string]string{"MCP_HELPER_PROCESS": "1"}
	for k, v := range extraEnv {
		env[k] = v
	}
	args := append([]string{"-test.run=^TestMCPHelperProcess$"}, extraArgs...)
	return ServerConfig{Command: helperExe(t), Args: args, Env: env, TimeoutSeconds: 30}
}

// newTestHost builds a Host over a temp workspace.
func newTestHost(t *testing.T, servers map[string]ServerConfig) (*Host, string) {
	t.Helper()
	ws := t.TempDir()
	h, err := New(Config{Servers: servers}, ws)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, ws
}

// shrinkGrace shortens shutdown waits for kill-path tests.
func shrinkGrace(t *testing.T) {
	t.Helper()
	og, ot := hostShutdownGrace, hostTerminateWait
	hostShutdownGrace, hostTerminateWait = 100*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { hostShutdownGrace, hostTerminateWait = og, ot })
}

// waitForCondition polls cond until true or timeout. Process death and
// file growth are inherently asynchronous; bounded polling with generous
// timeouts is deterministic enough (repo precedent: waitGone).
func waitForCondition(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("timed out waiting for %s", what)
	}
}

func snapshotOf(h *Host, key string) ServerSnapshot {
	for _, ss := range h.Snapshot().Servers {
		if ss.Key == key {
			return ss
		}
	}
	return ServerSnapshot{}
}

func TestBuildEnvMinimal(t *testing.T) {
	t.Setenv("MCP_HOST_CANARY_UNSET_XYZ", "present-in-parent")
	cfg := ServerConfig{Env: map[string]string{"EXPLICIT": "1"}}
	env := buildEnv(cfg)
	for _, e := range env {
		if strings.HasPrefix(e, "MCP_HOST_CANARY_UNSET_XYZ=") {
			t.Errorf("parent environment leaked wholesale: %q", e)
		}
	}
	found := false
	for _, e := range env {
		if e == "EXPLICIT=1" {
			found = true
		}
	}
	if !found {
		t.Errorf("explicit env missing from %v", env)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("SystemRoot", `C:\Windows`)
		env = buildEnv(cfg)
		found = false
		for _, e := range env {
			if e == `SystemRoot=C:\Windows` {
				found = true
			}
		}
		if !found {
			t.Errorf("Windows baseline must preserve SystemRoot, got %v", env)
		}
	} else if len(env) != 1 {
		t.Errorf("Unix baseline must be exactly the explicit entry, got %v", env)
	}
}

func TestBuildEnvPassthroughAndOverride(t *testing.T) {
	t.Setenv("MCP_HOST_PASS_A", "from-parent")
	t.Setenv("MCP_HOST_PASS_B", "from-parent")
	cfg := ServerConfig{
		EnvPassthrough: []string{"MCP_HOST_PASS_A", "MCP_HOST_PASS_B", "MCP_HOST_ABSENT_XYZ"},
		Env:            map[string]string{"MCP_HOST_PASS_B": "explicit-wins"},
	}
	env := buildEnv(cfg)
	got := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		got[k] = v
	}
	if got["MCP_HOST_PASS_A"] != "from-parent" {
		t.Errorf("passthrough copy missing: %v", got)
	}
	if got["MCP_HOST_PASS_B"] != "explicit-wins" {
		t.Errorf("explicit env must win over passthrough: %v", got)
	}
	if _, ok := got["MCP_HOST_ABSENT_XYZ"]; ok {
		t.Errorf("absent passthrough must be skipped, got %v", got)
	}
}

func TestBuildEnvNoExpansion(t *testing.T) {
	cfg := ServerConfig{Env: map[string]string{
		"WEIRD": `$HOME/${USER}/%SystemRoot%/` + "`echo hi`",
	}}
	env := buildEnv(cfg)
	want := `$HOME/${USER}/%SystemRoot%/` + "`echo hi`"
	found := false
	for _, e := range env {
		if e == "WEIRD="+want {
			found = true
		}
	}
	if !found {
		t.Errorf("env value must stay literal, got %v", env)
	}
}

func TestResolveExecutable(t *testing.T) {
	dir := t.TempDir()
	name := "fakeprog"
	path := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		path += ".exe"
		if err := os.WriteFile(path, []byte("MZ"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	withPath := ServerConfig{Env: map[string]string{"PATH": dir}}
	got, err := resolveExecutable("t", name, dir, withPath)
	if err != nil {
		t.Fatalf("PATH lookup: %v", err)
	}
	// Windows filesystems match case-insensitively: compare accordingly.
	if runtime.GOOS == "windows" {
		if !strings.EqualFold(got, path) {
			t.Errorf("resolved = %q, want %q (case-insensitive)", got, path)
		}
	} else if got != path {
		t.Errorf("resolved = %q, want %q", got, path)
	}
	// No PATH in the constructed env: actionable failure, never the
	// parent PATH.
	if _, err := resolveExecutable("t", name, dir, ServerConfig{}); err == nil {
		t.Error("bare name without constructed PATH accepted")
	} else if !strings.Contains(err.Error(), "env_passthrough") {
		t.Errorf("error %q must point at env_passthrough", err)
	}
	// Absolute missing path fails.
	if _, err := resolveExecutable("t", filepath.Join(dir, "nope"), dir, ServerConfig{}); err == nil {
		t.Error("missing absolute command accepted")
	}
	// Relative spelling anchors to the launch dir.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	relFull := filepath.Join(sub, name)
	rel := "sub/" + name
	if runtime.GOOS == "windows" {
		// Path spellings take no PATHEXT search (mirroring LookPath):
		// the extension must be explicit.
		rel = `sub\` + name + ".exe"
		relFull += ".exe"
	}
	if err := os.WriteFile(relFull, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = resolveExecutable("t", rel, dir, ServerConfig{})
	if err != nil {
		t.Fatalf("relative command: %v", err)
	}
	if runtime.GOOS == "windows" {
		if !strings.EqualFold(got, relFull) {
			t.Errorf("resolved = %q, want %q (case-insensitive)", got, relFull)
		}
	} else if got != relFull {
		t.Errorf("resolved = %q, want %q", got, relFull)
	}
}

func TestResolveDir(t *testing.T) {
	ws := t.TempDir()
	s := &server{key: "t", cfg: ServerConfig{}}
	if got, err := s.resolveDir(ws); err != nil || got != ws {
		t.Errorf("empty cwd = %q, %v; want workspace", got, err)
	}
	s.cfg.Cwd = filepath.Join("no-such-dir-xyz")
	if _, err := s.resolveDir(ws); err == nil {
		t.Error("missing cwd accepted")
	}
	f := filepath.Join(ws, "file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.cfg.Cwd = f
	if _, err := s.resolveDir(ws); err == nil {
		t.Error("file cwd accepted")
	}
	sub := filepath.Join(ws, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	s.cfg.Cwd = "sub"
	got, err := s.resolveDir(ws)
	if err != nil {
		t.Fatalf("relative cwd: %v", err)
	}
	want, _ := filepath.EvalSymlinks(sub)
	if got != want {
		t.Errorf("cwd = %q, want canonical %q", got, want)
	}
}

func TestNewRejects(t *testing.T) {
	if _, err := New(Config{}, filepath.Join("relative", "ws")); err == nil {
		t.Error("relative workspace accepted")
	}
	if _, err := New(Config{}, filepath.Join(os.TempDir(), "no-such-ws-xyz")); err == nil {
		t.Error("missing workspace accepted")
	}
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{}, f); err == nil {
		t.Error("file workspace accepted")
	}
	servers := map[string]ServerConfig{}
	for i := 0; i < MaxServers+1; i++ {
		servers["s"] = ServerConfig{Command: "x"}
		servers["s"+strings.Repeat("a", i+1)] = ServerConfig{Command: "x"}
	}
	if _, err := New(Config{Servers: servers}, t.TempDir()); err == nil {
		t.Error("over-budget config accepted")
	}
}

func TestFindDuplicateQualified(t *testing.T) {
	if got := findDuplicateQualified(map[string][]string{
		"mcp__a__x": {"a"},
		"mcp__b__y": {"b"},
	}); len(got) != 0 {
		t.Errorf("duplicates = %v, want none", got)
	}
	got := findDuplicateQualified(map[string][]string{
		"mcp__a__x": {"a", "a"},
		"mcp__s__y": {"s1", "s2"},
	})
	if len(got) != 1 || got[0] != "mcp__s__y" {
		t.Errorf("duplicates = %v, want [mcp__s__y]", got)
	}
}

func TestHostDisabledNeverSpawns(t *testing.T) {
	off := false
	cfg := ServerConfig{Command: "/nonexistent-xyz-123", Enabled: &off}
	h, _ := newTestHost(t, map[string]ServerConfig{"off": cfg})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	ss := snapshotOf(h, "off")
	if !ss.Skipped || ss.Started || ss.Ready {
		t.Errorf("disabled snapshot = %+v, want skipped unstarted", ss)
	}
}

func TestHostArgvSeparate(t *testing.T) {
	cfg := helperServerConfig(t, map[string]string{"MCP_HELPER_ARGV_DUMP": "1"}, "a b", ";rm", "x$y")
	h, _ := newTestHost(t, map[string]ServerConfig{"s": cfg})
	err := h.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded for argv-dump helper that never handshakes")
	}
	var raw []string
	if err := json.Unmarshal([]byte(snapshotOf(h, "s").Stderr), &raw); err != nil {
		t.Fatalf("argv dump %q: %v", snapshotOf(h, "s").Stderr, err)
	}
	// os.Args[0] plus the filter plus our three metacharacter args,
	// byte-identical: a shell would have split or expanded them.
	want := []string{"a b", ";rm", "x$y"}
	if len(raw) < len(want)+1 {
		t.Fatalf("argv = %v", raw)
	}
	for i, w := range want {
		if raw[len(raw)-len(want)+i] != w {
			t.Errorf("argv = %v, want trailing %v", raw, want)
		}
	}
}

// stderrCwd extracts the helper's reported working directory (the
// "cwd=<dir>" line) from captured stderr, or "" when absent.
func stderrCwd(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "cwd="); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// sameDir reports whether two paths name the same directory, resolving
// symlinks first. Processes observe the physical path via os.Getwd,
// while callers may hold an unresolved spelling (macOS t.TempDir
// returns /var/... for /private/var/...); comparing canonical forms
// keeps the assertion about the working directory, not its spelling.
// Unresolvable paths compare literally, so a missing cwd line never
// matches a real workspace.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ca, errA := filepath.EvalSymlinks(a)
	cb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return ca == cb
}

func TestHostCwdApplied(t *testing.T) {
	h, ws := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_STDERR_PWD": "1"}),
	})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	ss := snapshotOf(h, "s")
	if !ss.Ready {
		t.Fatalf("not ready: %s", ss.LastError)
	}
	if ss.Dir != ws {
		t.Errorf("Dir = %q, want workspace %q", ss.Dir, ws)
	}
	if got := stderrCwd(ss.Stderr); !sameDir(got, ws) {
		t.Errorf("helper cwd %q is not the workspace %q (stderr %q)", got, ws, ss.Stderr)
	}
}

// TestHostCwdSymlinkedWorkspace runs the same cwd proof through a
// symlinked workspace spelling. POSIX kernels report the physical path
// via getwd while the Host holds the unresolved spelling (the macOS
// /var vs /private/var shape); the canonical comparison must accept
// both spellings of the same directory on every platform.
func TestHostCwdSymlinkedWorkspace(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	h, err := New(Config{Servers: map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_STDERR_PWD": "1"}),
	}}, link)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	ss := snapshotOf(h, "s")
	if !ss.Ready {
		t.Fatalf("not ready: %s", ss.LastError)
	}
	if got := stderrCwd(ss.Stderr); !sameDir(got, link) {
		t.Errorf("helper cwd %q is not the workspace %q (stderr %q)", got, link, ss.Stderr)
	}
}

func TestHostStartSuccess(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{"demo": helperServerConfig(t, nil)})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	ss := snapshotOf(h, "demo")
	if !ss.Ready || !ss.Healthy || ss.LastError != "" {
		t.Errorf("snapshot = %+v", ss)
	}
	if len(ss.Tools) != 1 || ss.Tools[0].Name() != "mcp__demo__echo" {
		t.Fatalf("tools = %v", toolNames(ss.Tools))
	}
	res, err := ss.Tools[0].Execute(context.Background(), map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !strings.Contains(res.Content, "echo:") {
		t.Errorf("content = %q", res.Content)
	}
	all := h.Tools()
	if len(all) != 1 || all[0].Name() != "mcp__demo__echo" {
		t.Errorf("host tools = %v", toolNames(all))
	}
}

func toolNames(ts []*Tool) []string {
	out := make([]string, 0, len(ts))
	for _, x := range ts {
		out = append(out, x.Name())
	}
	return out
}

func TestHostInitFailureKeepsOthers(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{
		"bad":  helperServerConfig(t, map[string]string{"MCP_HELPER_VERSION": "bogus-1"}),
		"good": helperServerConfig(t, nil),
	})
	err := h.Start(context.Background())
	var serr *StartError
	if !errors.As(err, &serr) {
		t.Fatalf("err = %v, want *StartError", err)
	}
	if len(serr.Failed) != 1 || serr.Failed[0] != "bad" {
		t.Errorf("failed = %v", serr.Failed)
	}
	if good := snapshotOf(h, "good"); !good.Ready {
		t.Errorf("healthy server not kept running: %+v", good)
	}
	bad := snapshotOf(h, "bad")
	if bad.Ready || !bad.Started || bad.LastError == "" {
		t.Errorf("bad snapshot = %+v", bad)
	}
}

func TestHostDiscoveryFailure(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_LIST_ERROR": "1"}),
	})
	if err := h.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded despite list error")
	}
	ss := snapshotOf(h, "s")
	if ss.Ready || !ss.Started || ss.LastError == "" {
		t.Errorf("snapshot = %+v", ss)
	}
}

func TestHostMalformedToolSkipped(t *testing.T) {
	tools := `{"name":"ok","description":"fine","inputSchema":{"type":"object"}},{"name":""}`
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_TOOLS_JSON": tools}),
	})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v (one bad tool must not fail the server)", err)
	}
	ss := snapshotOf(h, "s")
	if len(ss.Tools) != 1 || ss.Tools[0].RemoteName() != "ok" {
		t.Errorf("tools = %v", toolNames(ss.Tools))
	}
}

func TestHostExitEarly(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_EXIT_EARLY": "3"}),
	})
	if err := h.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded for immediately-exiting server")
	}
	ss := snapshotOf(h, "s")
	if !ss.Exited || ss.ExitCode != 3 || ss.Ready {
		t.Errorf("snapshot = %+v", ss)
	}
}

func TestHostStartupTimeout(t *testing.T) {
	shrinkGrace(t)
	cfg := helperServerConfig(t, map[string]string{"MCP_HELPER_HANG": "1"})
	cfg.TimeoutSeconds = 2
	h, _ := newTestHost(t, map[string]ServerConfig{"s": cfg})
	start := time.Now()
	err := h.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded for hanging server")
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("startup took %v, far beyond the 2s server budget", time.Since(start))
	}
	ss := snapshotOf(h, "s")
	if !strings.Contains(ss.LastError, "deadline") {
		t.Errorf("LastError = %q, want deadline cause", ss.LastError)
	}
	if !ss.Exited {
		t.Error("timed-out server was not reaped (orphan risk)")
	}
}

func TestHostCrashAfterInit(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_CRASH_AFTER_INIT": "1"}),
	})
	if err := h.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded for crashing server")
	}
	ss := snapshotOf(h, "s")
	if !ss.Exited || ss.ExitCode != 1 || ss.Ready {
		t.Errorf("snapshot = %+v", ss)
	}
	// No respawn, ever: the server must stay down.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if snapshotOf(h, "s").Ready {
			t.Fatal("crashed server became ready: respawn detected")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestHostCloseDuringStartup(t *testing.T) {
	shrinkGrace(t)
	cfg := helperServerConfig(t, map[string]string{"MCP_HELPER_HANG": "1"})
	cfg.TimeoutSeconds = 60
	h, _ := newTestHost(t, map[string]ServerConfig{"s": cfg})
	ch := make(chan error, 1)
	go func() { ch <- h.Start(context.Background()) }()
	// Wait for the spawn (not readiness): then Close must abort promptly.
	waitForCondition(t, 10*time.Second, "server spawn", func() bool {
		return snapshotOf(h, "s").Started
	})
	_ = h.Close()
	select {
	case err := <-ch:
		if err == nil {
			t.Error("Start returned nil after Close during startup")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not abort after Close")
	}
	ss := snapshotOf(h, "s")
	if !ss.Exited {
		t.Error("aborted startup left the process unreaped")
	}
}

func TestHostNormalClose(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{"s": helperServerConfig(t, nil)})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	tools := h.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %v", toolNames(tools))
	}
	if err := h.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second Close error = %v, want idempotent nil", err)
	}
	ss := snapshotOf(h, "s")
	if ss.Ready || ss.Healthy || !ss.Exited || ss.ExitCode != 0 {
		t.Errorf("post-close snapshot = %+v", ss)
	}
	if _, err := tools[0].Execute(context.Background(), map[string]any{}); !errors.Is(err, ErrClosed) {
		t.Errorf("post-close execute err = %v, want ErrClosed", err)
	}
}

func TestHostProcessDiesAfterReady(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_DIE_ON_CALL": "1"}),
	})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	tools := h.Tools()
	if _, err := tools[0].Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("call into dying server succeeded")
	}
	waitForCondition(t, 5*time.Second, "crash detection", func() bool {
		ss := snapshotOf(h, "s")
		return ss.Exited && !ss.Healthy && !ss.Ready
	})
	ss := snapshotOf(h, "s")
	if ss.ExitCode != 1 {
		t.Errorf("exit code = %d", ss.ExitCode)
	}
	if _, err := tools[0].Execute(context.Background(), map[string]any{}); !errors.Is(err, ErrTransport) {
		t.Errorf("post-crash execute err = %v, want fail-fast ErrTransport", err)
	}
	// Stability: still down shortly after, i.e. no respawn loop.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if snapshotOf(h, "s").Ready {
			t.Fatal("dead server became ready: respawn detected")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestHostTreeCleanup(t *testing.T) {
	log := filepath.Join(t.TempDir(), "heart.log")
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{
			"MCP_HELPER_TREE": "1",
			"MCP_HELPER_LOG":  log,
		}),
	})
	// The tree helper never handshakes; start it via Start in the
	// background and wait for grandchild heartbeats instead of readiness.
	ch := make(chan error, 1)
	go func() { ch <- h.Start(context.Background()) }()
	waitForCondition(t, 10*time.Second, "grandchild heartbeat", func() bool {
		b, err := os.ReadFile(log)
		return err == nil && len(b) > 0
	})
	_ = h.Close()
	// Descendant quiet: no ticks for a full second (five missed beats).
	size := fileSize(log)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if s := fileSize(log); s != size {
			t.Fatalf("grandchild survived Host termination: log grew %d -> %d", size, s)
		}
	}
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after Close")
	}
}

func fileSize(p string) int64 {
	info, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return info.Size()
}

func TestHostStderrBounded(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_STDERR_FILL": "256"}),
	})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v (stderr volume must not wedge stdout)", err)
	}
	ss := snapshotOf(h, "s")
	if !ss.Ready {
		t.Fatalf("not ready: %s", ss.LastError)
	}
	if len(ss.Stderr) != maxStderrBytes {
		t.Errorf("stderr tail = %d bytes, want capped %d", len(ss.Stderr), maxStderrBytes)
	}
	if strings.Trim(ss.Stderr, "E") != "" {
		t.Error("stderr tail is not the most recent fill bytes")
	}
	// The transport survived the flood: a call still works.
	res, err := ss.Tools[0].Execute(context.Background(), map[string]any{})
	if err != nil || !strings.Contains(res.Content, "echo:") {
		t.Errorf("post-flood call res = %+v err = %v", res, err)
	}
}

func TestHostSecretRedaction(t *testing.T) {
	const secret = "s3cr3t-value-abc123xyz"
	cfg := helperServerConfig(t, map[string]string{
		"MCP_HELPER_VERSION":   "bogus-1",
		"MCP_HOST_API_TOKEN_X": secret,
	})
	h, _ := newTestHost(t, map[string]ServerConfig{"s": cfg})
	if err := h.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded despite bogus version")
	}
	ss := snapshotOf(h, "s")
	if strings.Contains(ss.LastError, secret) || strings.Contains(ss.Stderr, secret) {
		t.Errorf("secret leaked into diagnostics: %q %q", ss.LastError, ss.Stderr)
	}
	if got := redact.Scrub(secret); got == secret {
		t.Error("secret-looking explicit env value was not registered for redaction")
	}
}

func TestHostTimeoutPropagation(t *testing.T) {
	fast := helperServerConfig(t, nil)
	fast.TimeoutSeconds = 45
	h, _ := newTestHost(t, map[string]ServerConfig{
		"a": fast,
		"b": helperServerConfig(t, nil),
	})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	for _, ss := range h.Snapshot().Servers {
		if len(ss.Tools) != 1 {
			t.Fatalf("%s tools = %v", ss.Key, toolNames(ss.Tools))
		}
		got := ss.Tools[0].ToolLimits().Timeout
		want := 30 * time.Second
		if ss.Key == "a" {
			want = 45 * time.Second
		}
		if got != want {
			t.Errorf("%s timeout = %v, want %v (Phase 4 finding: timeout_seconds must reach the adapter)", ss.Key, got, want)
		}
	}
}

func TestHostDuplicateQualifiedReported(t *testing.T) {
	// Same tool name on two servers qualifies differently by server key:
	// both stay usable, and no phantom duplicate is reported.
	h, _ := newTestHost(t, map[string]ServerConfig{
		"a": helperServerConfig(t, nil),
		"b": helperServerConfig(t, nil),
	})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	snap := h.Snapshot()
	if len(snap.Duplicates) != 0 {
		t.Errorf("duplicates = %v, want none for distinct server keys", snap.Duplicates)
	}
	if len(h.Tools()) != 2 {
		t.Errorf("tools = %v", toolNames(h.Tools()))
	}
}

func TestHostBoundedStatus(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{
			"MCP_HELPER_STDERR_FILL":      "256",
			"MCP_HELPER_CRASH_AFTER_INIT": "1",
		}),
	})
	if err := h.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded for crashing server")
	}
	ss := snapshotOf(h, "s")
	if len(ss.Stderr) > maxStderrBytes {
		t.Errorf("stderr snapshot = %d bytes, exceeds ring cap", len(ss.Stderr))
	}
	if len([]rune(ss.LastError)) > MaxErrorDetailRunes+256 {
		t.Errorf("LastError of %d runes is unbounded", len([]rune(ss.LastError)))
	}
}

func TestHostInvalidCwd(t *testing.T) {
	cfg := helperServerConfig(t, nil)
	cfg.Cwd = filepath.Join(t.TempDir(), "no-such-dir-xyz")
	h, _ := newTestHost(t, map[string]ServerConfig{"s": cfg})
	if err := h.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded with missing cwd")
	}
	ss := snapshotOf(h, "s")
	if ss.Started || ss.Ready {
		t.Errorf("snapshot = %+v, invalid cwd must fail before spawn", ss)
	}
	if !strings.Contains(ss.LastError, "cwd") {
		t.Errorf("LastError = %q, want cwd cause", ss.LastError)
	}
}

func TestHostStartErrorShape(t *testing.T) {
	serr := &StartError{Failed: []string{"b", "a"}}
	// Construction sorts; direct literal preserves order for message test.
	if !strings.Contains(serr.Error(), "b") || !strings.Contains(serr.Error(), "a") {
		t.Errorf("message = %q", serr.Error())
	}
	var target *StartError
	if !errors.As(serr, &target) {
		t.Error("StartError must survive errors.As")
	}
}
