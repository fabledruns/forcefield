package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"forcefield/internal/sandbox"
)

// markerHook writes an executable script that appends HIT to marker when
// run, and returns the repo config value that invokes it directly
// (absolute path, no shell). If any git action executes repo-configured
// helpers, the marker appears.
func markerHook(t *testing.T, dir, marker string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		hook := filepath.Join(dir, ".git", "marker-hook.bat")
		body := "@echo off\r\necho HIT>>\"" + marker + "\"\r\n"
		if err := os.WriteFile(hook, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return hook
	}
	hook := filepath.Join(dir, ".git", "marker-hook.sh")
	body := "#!/bin/sh\necho HIT >> \"" + marker + "\"\n"
	if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return hook
}

func gitIn(t *testing.T, dir string, argv ...string) string {
	t.Helper()
	cmd := exec.Command("git", argv...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v (%s)", argv, err, out)
	}
	return string(out)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func hardeningRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	writeRepoFile(t, dir, "seed.txt", "one\n")
	initRepo(t, dir)
	return dir
}

func toolFor(dir string) Git {
	return *NewGitWithPolicy(sandbox.Policy{Workspace: dir})
}

// A repo configuring core.fsmonitor must not execute it on status: the
// -c core.fsmonitor= override neutralizes it on every invocation.
func TestGit_FsmonitorNotExecuted(t *testing.T) {
	dir := hardeningRepo(t)
	marker := filepath.Join(dir, "MARKER")
	hook := markerHook(t, dir, marker)
	gitIn(t, dir, "config", "core.fsmonitor", hook)

	tool := toolFor(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("repo core.fsmonitor executed during status")
	}
	if res.IsError {
		t.Fatalf("status with neutralized fsmonitor must succeed, got: %s", res.Content)
	}
}

// A repo configuring a textconv driver must not execute it on diff:
// --no-textconv disables it on every diff invocation.
func TestGit_TextconvNotExecuted(t *testing.T) {
	dir := hardeningRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.txt diff=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "MARKER")
	hook := markerHook(t, dir, marker)
	gitIn(t, dir, "config", "diff.evil.textconv", hook)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-qm", "attrs")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := toolFor(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"action": "diff"})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("repo textconv driver executed during diff")
	}
	if res.IsError || !strings.Contains(res.Content, "two") {
		t.Fatalf("diff with neutralized textconv must show the change, got: %+v", res)
	}
}

// A repo configuring an external diff must not execute it: --no-ext-diff
// stays on every diff invocation (pinned against future flag drift).
func TestGit_ExternalDiffNotExecuted(t *testing.T) {
	dir := hardeningRepo(t)
	marker := filepath.Join(dir, "MARKER")
	hook := markerHook(t, dir, marker)
	gitIn(t, dir, "config", "diff.external", hook)
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := toolFor(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"action": "diff"})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("repo diff.external executed during diff")
	}
	if res.IsError {
		t.Fatalf("diff with neutralized external driver must succeed, got: %s", res.Content)
	}
}

// Content filters (clean/smudge) ARE reached by read-only inspection:
// status and unstaged diff hash worktree content through the clean
// filter. The tool therefore neutralizes every configured driver
// (clean/smudge/process set to cat) instead of assuming distance.
// A repo defining them must stay inert across status and diff while
// inspection keeps working on raw content.
func TestGit_FilterNotExecuted(t *testing.T) {
	dir := hardeningRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.txt filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "MARKER")
	hook := markerHook(t, dir, marker)
	gitIn(t, dir, "config", "filter.evil.clean", hook)
	gitIn(t, dir, "config", "filter.evil.smudge", hook)

	tool := toolFor(dir)
	for _, action := range []string{"status", "diff", "staged", "changed"} {
		if _, err := tool.Execute(context.Background(), map[string]any{"action": action}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("repo content filter executed during read-only inspection")
	}
}

// Builtin command words ignore [alias] overrides by git design: only
// non-builtin words expand aliases, and the tool invokes builtins
// exclusively. A repo aliasing status/diff to shell must stay inert.
func TestGit_BuiltinAliasOverrideInert(t *testing.T) {
	dir := hardeningRepo(t)
	marker := filepath.Join(dir, "MARKER")
	hook := markerHook(t, dir, marker)
	gitIn(t, dir, "config", "alias.status", "!"+hook)
	gitIn(t, dir, "config", "alias.diff", "!"+hook)

	tool := toolFor(dir)
	for _, action := range []string{"status", "diff"} {
		res, err := tool.Execute(context.Background(), map[string]any{"action": action})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if res.IsError {
			t.Fatalf("%s with alias override must succeed, got: %s", action, res.Content)
		}
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("repo [alias] override executed during inspection")
	}
}

// The child environment is the allowlisted minimum plus forced
// neutralizations: sentinel and redirection variables never cross.
func TestGitEnv_Sanitized(t *testing.T) {
	t.Setenv("FF_GIT_CANARY_XYZ", "present")
	t.Setenv("GIT_DIR", "/bogus")
	t.Setenv("GIT_WORK_TREE", "/bogus")
	env := gitEnv()
	joined := "\x00" + strings.Join(env, "\x00") + "\x00"
	for _, bad := range []string{"FF_GIT_CANARY_XYZ", "GIT_DIR=", "GIT_WORK_TREE=", "GIT_CONFIG_COUNT="} {
		if strings.Contains(joined, "\x00"+bad) {
			t.Errorf("gitEnv leaks %q: %v", bad, env)
		}
	}
	for _, want := range []string{"GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat"} {
		found := false
		for _, kv := range env {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Errorf("gitEnv missing forced %q: %v", want, env)
		}
	}
}

// A poisoned GIT_DIR in the parent environment must not redirect
// inspection away from the workspace root.
func TestGit_PoisonedGitDirIgnored(t *testing.T) {
	dir := hardeningRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(dir, "nonexistent.git"))
	tool := toolFor(dir)
	res, err := tool.Execute(context.Background(), map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("poisoned GIT_DIR must not break inspection, got: %s", res.Content)
	}
}

// Cancelled contexts fail fast instead of hanging in a git child.
func TestGit_CancelledCtx(t *testing.T) {
	dir := hardeningRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := toolFor(dir)
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := tool.Execute(ctx, map[string]any{"action": "status"})
		if err != nil {
			t.Errorf("Execute error = %v", err)
			return
		}
		if !res.IsError {
			t.Errorf("cancelled git action must be a soft error, got %+v", res)
		}
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled git action hung")
	}
}
