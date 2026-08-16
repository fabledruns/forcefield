package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepo creates a minimal Git repository rooted at dir, skipping
// the test if git isn't available in the environment.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
}

func TestProjectRootInsideGitRepo(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	sub := filepath.Join(repoRoot, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	root, err := ProjectRoot(sub)
	if err != nil {
		t.Fatalf("ProjectRoot: %v", err)
	}

	wantRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	gotRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("ProjectRoot from a subdirectory = %q, want repo root %q", gotRoot, wantRoot)
	}
}

func TestProjectRootFallsBackToDirOutsideGit(t *testing.T) {
	dir := t.TempDir()

	root, err := ProjectRoot(dir)
	if err != nil {
		t.Fatalf("ProjectRoot: %v", err)
	}

	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Fatalf("ProjectRoot outside a repo = %q, want cwd %q", got, want)
	}
}

func TestProjectStoreIsScopedPerProject(t *testing.T) {
	home := t.TempDir()

	projectA := filepath.Join(t.TempDir(), "project-a")
	projectB := filepath.Join(t.TempDir(), "project-b")
	if err := os.MkdirAll(projectA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectB, 0o755); err != nil {
		t.Fatal(err)
	}

	storeA, err := ProjectStore(home, projectA)
	if err != nil {
		t.Fatalf("ProjectStore(a): %v", err)
	}
	storeB, err := ProjectStore(home, projectB)
	if err != nil {
		t.Fatalf("ProjectStore(b): %v", err)
	}

	if storeA.Path() == storeB.Path() {
		t.Fatalf("expected distinct projects to get distinct memory files, both got %q", storeA.Path())
	}

	if _, _, err := storeA.Add("fact only true for project A"); err != nil {
		t.Fatalf("Add to storeA: %v", err)
	}

	entriesA, err := storeA.Load()
	if err != nil {
		t.Fatalf("Load storeA: %v", err)
	}
	entriesB, err := storeB.Load()
	if err != nil {
		t.Fatalf("Load storeB: %v", err)
	}

	if len(entriesA) != 1 {
		t.Fatalf("expected 1 entry in project A, got %d", len(entriesA))
	}
	if len(entriesB) != 0 {
		t.Fatalf("expected project B's memory to be unaffected, got %d entries", len(entriesB))
	}
}

func TestProjectStoreIsStableForSameRoot(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	first, err := ProjectStore(home, project)
	if err != nil {
		t.Fatalf("ProjectStore (first): %v", err)
	}
	second, err := ProjectStore(home, project)
	if err != nil {
		t.Fatalf("ProjectStore (second): %v", err)
	}

	if first.Path() != second.Path() {
		t.Fatalf("expected the same project root to resolve to the same store, got %q vs %q", first.Path(), second.Path())
	}
}

func TestGlobalStoreIsSeparateFromProjectStore(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	global, err := GlobalStore(home)
	if err != nil {
		t.Fatalf("GlobalStore: %v", err)
	}
	proj, err := ProjectStore(home, project)
	if err != nil {
		t.Fatalf("ProjectStore: %v", err)
	}

	if global.Path() == proj.Path() {
		t.Fatalf("expected global and project stores to use different files")
	}

	if _, _, err := global.Add("applies to every project"); err != nil {
		t.Fatalf("Add to global: %v", err)
	}

	projEntries, err := proj.Load()
	if err != nil {
		t.Fatalf("Load project store: %v", err)
	}
	if len(projEntries) != 0 {
		t.Fatalf("expected writing to global memory to leave project memory untouched, got %d entries", len(projEntries))
	}
}
