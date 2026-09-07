package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
)

// TestResolveWorkspace_ExplicitRoot pins that a configured root wins,
// must exist, and canonicalizes.
func TestResolveWorkspace_ExplicitRoot(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Workspace.Root = dir
	cfg.Workspace.Mode = config.WorkspaceStrict

	root, err := ResolveWorkspace(cfg)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if root == "" {
		t.Fatal("empty root")
	}
	if !filepath.IsAbs(root) {
		t.Errorf("root %q is not absolute", root)
	}

	missing := &config.Config{}
	missing.Workspace.Root = filepath.Join(dir, "no-such-dir")
	if _, err := ResolveWorkspace(missing); err == nil {
		t.Error("missing explicit root must fail fast")
	} else if !strings.Contains(err.Error(), "workspace.root") {
		t.Errorf("error = %v, want it to name workspace.root", err)
	}

	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileCfg := &config.Config{}
	fileCfg.Workspace.Root = file
	if _, err := ResolveWorkspace(fileCfg); err == nil {
		t.Error("file-as-root must fail")
	}
}

// TestResolveWorkspace_EmptyRootGitFallback pins the fallback chain:
// inside a repo the Git top-level wins (even from a subdirectory),
// outside any repo the cwd wins.
func TestResolveWorkspace_EmptyRootGitFallback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	repo := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run(repo, "init", "-q")
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	root, err := ResolveWorkspace(&config.Config{})
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	// Git may return a differently-cased/spelled root; compare resolved.
	want, _ := filepath.EvalSymlinks(repo)
	got, _ := filepath.EvalSymlinks(root)
	if !strings.EqualFold(want, got) {
		t.Errorf("root = %q, want git top %q", root, repo)
	}

	// Outside any repo: the cwd itself. Point HOME away so no global
	// config interferes, and use a fresh dir.
	plain := t.TempDir()
	if err := os.Chdir(plain); err != nil {
		t.Fatal(err)
	}
	// Guard: the temp area itself might sit inside a repo (CI checkouts).
	if _, err := exec.Command("git", "-C", plain, "rev-parse").CombinedOutput(); err == nil {
		t.Skip("temp dir is inside a git repo; fallback untestable here")
	}
	root, err = ResolveWorkspace(&config.Config{})
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	// Compare canonical paths: on macOS t.TempDir() keeps the /var/...
	// spelling while os.Getwd() returns /private/var/... for the same
	// directory (/var is a symlink). Same normalization as the git branch.
	wantPlain, _ := filepath.EvalSymlinks(plain)
	gotRoot, _ := filepath.EvalSymlinks(root)
	if wantPlain == "" {
		wantPlain = plain
	}
	if gotRoot == "" {
		gotRoot = root
	}
	if !strings.EqualFold(wantPlain, gotRoot) {
		t.Errorf("root = %q, want cwd %q", root, plain)
	}
}

// TestNewPolicy_StrictFlowsThrough pins that workspace.mode=strict
// reaches the sandbox policy (and stays off otherwise), and that the
// resolved root lands on the policy and the runtime.
func TestNewPolicy_StrictFlowsThrough(t *testing.T) {
	dir := t.TempDir()
	strict := &config.Config{}
	strict.Workspace.Root = dir
	strict.Workspace.Mode = config.WorkspaceStrict
	pol, err := newPolicy(strict)
	if err != nil {
		t.Fatalf("newPolicy: %v", err)
	}
	if !pol.Strict || !pol.Confines() {
		t.Errorf("policy = %+v, want strict confining", pol)
	}
	if pol.Workspace == "" {
		t.Error("policy workspace empty")
	}

	plain := &config.Config{}
	pol, err = newPolicy(plain)
	if err != nil {
		t.Fatalf("newPolicy: %v", err)
	}
	if pol.Strict || pol.Confines() {
		t.Errorf("default policy = %+v, want permissive", pol)
	}
}
