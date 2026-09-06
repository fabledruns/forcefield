package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// initRepo creates a git repository with one committed file, skipping
// when git is unavailable.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, argv := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"add", "."},
		{"commit", "-qm", "initial"},
	} {
		cmd := exec.Command("git", argv...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", argv, err, out)
		}
	}
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupRepo builds dir/seed.txt committed, then a modification, a
// staged file, and an untracked file.
func setupRepo(t *testing.T) string {
	dir := t.TempDir()
	writeRepoFile(t, dir, "seed.txt", "one\n")
	initRepo(t, dir)
	writeRepoFile(t, dir, "seed.txt", "one\ntwo\n")       // unstaged modification
	writeRepoFile(t, dir, "staged.txt", "staged\n")       // to stage
	writeRepoFile(t, dir, "untracked.txt", "untracked\n") // untracked
	cmd := exec.Command("git", "add", "staged.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v (%s)", err, out)
	}
	return dir
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestGit_StatusShowsWorktreeState(t *testing.T) {
	dir := setupRepo(t)
	chdir(t, dir)
	tool := NewGit()

	res, err := tool.Execute(context.Background(), map[string]any{"action": "status"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	for _, want := range []string{"seed.txt", "staged.txt", "untracked.txt"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("status missing %q, got:\n%s", want, res.Content)
		}
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestGit_DiffStagedLogChanged(t *testing.T) {
	dir := setupRepo(t)
	chdir(t, dir)
	tool := NewGit()

	res, err := tool.Execute(context.Background(), map[string]any{"action": "diff"})
	if err != nil || res.IsError {
		t.Fatalf("diff: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "+two") {
		t.Errorf("unstaged diff missing hunk, got:\n%.500s", res.Content)
	}
	if strings.Contains(res.Content, "staged.txt") {
		t.Errorf("unstaged diff must not include staged file, got:\n%.500s", res.Content)
	}

	res, err = tool.Execute(context.Background(), map[string]any{"action": "staged"})
	if err != nil || res.IsError {
		t.Fatalf("staged: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "staged.txt") {
		t.Errorf("staged diff missing file, got:\n%.500s", res.Content)
	}

	res, err = tool.Execute(context.Background(), map[string]any{"action": "log"})
	if err != nil || res.IsError {
		t.Fatalf("log: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "initial") {
		t.Errorf("log missing commit, got:\n%s", res.Content)
	}

	res, err = tool.Execute(context.Background(), map[string]any{"action": "changed"})
	if err != nil || res.IsError {
		t.Fatalf("changed: %v %v", err, res.Content)
	}
	for _, want := range []string{"Modified:", "seed.txt", "Untracked:", "untracked.txt"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("changed missing %q, got:\n%s", want, res.Content)
		}
	}
}

func TestGit_CleanTreeReportsClean(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "a.txt", "x\n")
	initRepo(t, dir)
	chdir(t, dir)
	tool := NewGit()

	for _, action := range []string{"status", "diff", "staged", "changed"} {
		res, err := tool.Execute(context.Background(), map[string]any{"action": action})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %v", action, err, res.Content)
		}
		if !strings.Contains(res.Content, "clean") {
			t.Errorf("%s on clean tree = %q, want a clean note", action, res.Content)
		}
	}
}

func TestGit_ReadOnlyRejectsMutations(t *testing.T) {
	dir := setupRepo(t)
	chdir(t, dir)
	tool := NewGit()

	for _, action := range []string{"commit", "add", "checkout", "reset", "clean", "push", "status; rm -rf /", ""} {
		res, err := tool.Execute(context.Background(), map[string]any{"action": action})
		if err != nil {
			t.Fatalf("%q: unexpected hard error %v", action, err)
		}
		if !res.IsError || !strings.Contains(res.Content, "read-only") {
			t.Errorf("%q must be refused as read-only, got: %q", action, res.Content)
		}
	}
	// The refusal above must not have mutated anything.
	res, err := tool.Execute(context.Background(), map[string]any{"action": "status"})
	if err != nil || res.IsError {
		t.Fatalf("status after refusals: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "seed.txt") {
		t.Errorf("worktree changed by refused actions, got:\n%s", res.Content)
	}
}

func TestGit_NotARepositoryIsSoftError(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tool := NewGit()
	res, err := tool.Execute(context.Background(), map[string]any{"action": "status"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "not a git repository") {
		t.Errorf("want not-a-repo soft error, got:\n%s", res.Content)
	}
}

func TestGit_MissingBinaryIsSoftError(t *testing.T) {
	old := gitBinary
	gitBinary = func(string) (string, error) { return "", exec.ErrNotFound }
	defer func() { gitBinary = old }()

	tool := NewGit()
	res, err := tool.Execute(context.Background(), map[string]any{"action": "status"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "not available") {
		t.Errorf("want missing-binary soft error, got:\n%s", res.Content)
	}
}

func TestGit_PathScopeAndBoundary(t *testing.T) {
	dir := setupRepo(t)
	chdir(t, dir)
	ws := dir
	tool := NewGitWithPolicy(sandbox.Policy{Mode: sandbox.ModeNative, Workspace: ws, Strict: true})

	// Scoped diff shows only the scoped file.
	res, err := tool.Execute(context.Background(), map[string]any{"action": "diff", "path": "seed.txt"})
	if err != nil || res.IsError {
		t.Fatalf("scoped diff: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "+two") {
		t.Errorf("scoped diff missing hunk, got:\n%.300s", res.Content)
	}

	// Absolute outside path is denied.
	outside := filepath.Join(t.TempDir(), "evil.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = tool.Execute(context.Background(), map[string]any{"action": "diff", "path": outside})
	if err != nil || !res.IsError {
		t.Fatalf("outside scope = %+v err=%v, want denial", res, err)
	}

	// Traversal is denied.
	res, err = tool.Execute(context.Background(), map[string]any{"action": "status", "path": ".."})
	if err != nil || !res.IsError {
		t.Fatalf("traversal scope = %+v err=%v, want denial", res, err)
	}
}

func TestGit_LimitValidation(t *testing.T) {
	dir := setupRepo(t)
	chdir(t, dir)
	tool := NewGit()
	for _, limit := range []string{"0", "51", "abc", "-3"} {
		res, err := tool.Execute(context.Background(), map[string]any{"action": "log", "limit": limit})
		if err != nil || !res.IsError {
			t.Fatalf("limit %q = %+v err=%v, want soft rejection", limit, res, err)
		}
	}
	res, err := tool.Execute(context.Background(), map[string]any{"action": "log", "limit": "1"})
	if err != nil || res.IsError {
		t.Fatalf("limit 1 = %+v err=%v, want success", res, err)
	}
}

func TestGit_OutputBounded(t *testing.T) {
	dir := setupRepo(t)
	chdir(t, dir)
	// Stage a small file, then grow it: the unstaged diff is large.
	writeRepoFile(t, dir, "big.txt", "header\n")
	cmd := exec.Command("git", "add", "big.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v (%s)", err, out)
	}
	tool := NewGit()
	tool.SetLimits(tools.Limits{MaxBytes: 4096})
	// Grow the file after staging so the unstaged diff is large.
	var b2 strings.Builder
	for i := 0; i < 20000; i++ {
		b2.WriteString("changed line content here\n")
	}
	writeRepoFile(t, dir, "big.txt", b2.String())

	res, err := tool.Execute(context.Background(), map[string]any{"action": "diff"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	if len(res.Content) > 4096+512 {
		t.Errorf("content %d bytes exceeds the 4 KiB override", len(res.Content))
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Error("bounded diff lacks a truncation marker")
	}
	if res.Metadata == nil || res.Metadata["truncated"] != true {
		t.Errorf("Metadata = %v, want truncation record", res.Metadata)
	}
}

func TestGit_ToolLimitsDefaults(t *testing.T) {
	got := NewGit().ToolLimits()
	if got.MaxBytes != tools.DefaultGitMaxBytes {
		t.Errorf("MaxBytes = %d, want %d", got.MaxBytes, tools.DefaultGitMaxBytes)
	}
}
