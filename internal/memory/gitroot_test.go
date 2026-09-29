package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// canon canonicalizes p for comparison: both implementations must agree
// on the physical spelling.
func canon(t *testing.T, p string) string {
	t.Helper()
	if p == "" {
		return ""
	}
	c, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return c
}

// assertParity asserts gitRoot(dir) agrees exactly with the historical
// subprocess implementation. This is the optimization's contract: the
// fast path may only ever agree with git, never contradict it. Roots
// must match BYTE-FOR-BYTE (not just canonically): the spelling feeds
// project identity hashes, so a differently-spelled same directory
// would silently relocate a project's memory file.
func assertParity(t *testing.T, dir string) {
	t.Helper()
	gotRoot, gotOK := gitRoot(dir)
	wantRoot, wantOK := gitRootSpawn(dir)
	if gotOK != wantOK || gotRoot != wantRoot {
		t.Errorf("gitRoot(%q) = (%q, %v), want subprocess result (%q, %v)",
			dir, gotRoot, gotOK, wantRoot, wantOK)
	}
	if gotOK && canon(t, gotRoot) != canon(t, wantRoot) {
		t.Errorf("gitRoot(%q) = %q names a different directory than subprocess %q",
			dir, gotRoot, wantRoot)
	}
}

func TestGitRootParityInRepo(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)

	sub := filepath.Join(repoRoot, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{repoRoot, filepath.Join(repoRoot, "a"), sub} {
		assertParity(t, dir)
	}
	// The fast path must actually engage here, not just fall back,
	// spelling its answer byte-identically to the subprocess (project
	// identity hashes the raw string).
	wantRoot, wantOK := gitRootSpawn(sub)
	if !wantOK {
		t.Fatalf("gitRootSpawn(sub) = (%q, false), want a root", wantRoot)
	}
	if root, ok, certain := gitRootFast(sub); !ok || !certain || root != wantRoot {
		t.Errorf("gitRootFast(sub) = (%q, %v, certain=%v), want (%q, true, true)", root, ok, certain, wantRoot)
	}
}

func TestGitRootParityOutsideRepo(t *testing.T) {
	// Agreement, not specific values: even if Temp ever sat inside a
	// repo, both implementations must still concur.
	assertParity(t, t.TempDir())
	assertParity(t, filepath.Join(t.TempDir(), "does", "not", "exist"))
}

func TestGitRootMissSpawnsNothing(t *testing.T) {
	// A directory outside any repo on a single-volume ancestor chain
	// must resolve without spawning git. Swap the spawner for a
	// tripwire: any call fails the test. No test in this package runs
	// in parallel, so the swap is safe.
	dir := t.TempDir()
	if !singleVolumeToRoot(dir) {
		t.Skip("temp spans filesystems (e.g. tmpfs /tmp); the miss path spawns by design there")
	}
	calls := 0
	prev := gitRootSpawner
	gitRootSpawner = func(dir string) (string, bool) {
		calls++
		return "", false
	}
	defer func() { gitRootSpawner = prev }()

	// Outcome depends on Temp's ancestors: inside a repo the fast
	// path hits (no spawn); outside it misses definitively (no
	// spawn). Either way the subprocess must stay silent; parity
	// (including byte spelling) is asserted by assertParity.
	assertParity(t, dir)
	if calls != 0 {
		t.Errorf("gitRoot(%q) spawned git %d times", dir, calls)
	}
}

// singleVolumeToRoot reports whether every ancestor of dir up to the
// filesystem root shares one volume. Only then can the definitive
// miss avoid the subprocess; across a boundary the miss path spawns
// by design (matching git, which stops discovery there).
func singleVolumeToRoot(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for d := abs; ; {
		parent := filepath.Dir(d)
		if parent == d {
			return true
		}
		if !sameVolume(d, parent) {
			return false
		}
		d = parent
	}
}

func TestGitRootHitSpawnsNothing(t *testing.T) {
	// Inside a repo the answer must come from the fast path alone,
	// even with no git binary on PATH to spawn.
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)
	sub := filepath.Join(repoRoot, "a")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	prev := gitRootSpawner
	gitRootSpawner = func(dir string) (string, bool) {
		t.Errorf("gitRoot(%q) spawned git", dir)
		return "", false
	}
	defer func() { gitRootSpawner = prev }()
	t.Setenv("PATH", t.TempDir()) // git binary unreachable regardless

	root, ok := gitRoot(sub)
	if !ok || canon(t, root) != canon(t, repoRoot) {
		t.Errorf("gitRoot(sub) = (%q, %v), want (%q, true)", root, ok, repoRoot)
	}
}

func TestGitRootParityFileNotDir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertParity(t, f)
}

func TestGitRootParityNestedRepos(t *testing.T) {
	outer := t.TempDir()
	initGitRepo(t, outer)
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, inner)
	innerSub := filepath.Join(inner, "sub")
	if err := os.MkdirAll(innerSub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Nearest repo wins for both.
	assertParity(t, innerSub)
	wantInner, wantInnerOK := gitRootSpawn(innerSub)
	if !wantInnerOK {
		t.Fatalf("gitRootSpawn(innerSub) = (%q, false), want a root", wantInner)
	}
	if root, ok, certain := gitRootFast(innerSub); !ok || !certain || root != wantInner {
		t.Errorf("gitRootFast(inner/sub) = (%q, %v, certain=%v), want (%q, true, true)", root, ok, certain, wantInner)
	}
}

func TestGitRootFastFakeGitDirNeedsNoBinary(t *testing.T) {
	// A hand-made `.git` directory engages the fast path with no git
	// binary involved at all. It needs HEAD plus objects/ and refs/,
	// like real `git init` creates: verified against real git, a
	// `.git` with only HEAD is rejected as "not a git repository".
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root, ok, certain := gitRootFast(sub)
	if !ok || !certain || canon(t, root) != canon(t, dir) {
		t.Errorf("gitRootFast(sub) = (%q, %v, certain=%v), want (%q, true, true)", root, ok, certain, dir)
	}
}

func TestGitRootFastRefusesIncompleteGitDir(t *testing.T) {
	// Every subset of {HEAD, objects, refs} except the full triple
	// must defer: real git rejects each of them.
	full := map[string]bool{"HEAD": true, "objects": true, "refs": true}
	for _, tc := range []struct {
		name string
		keep map[string]bool
	}{
		{"head-only", map[string]bool{"HEAD": true}},
		{"head-objects", map[string]bool{"HEAD": true, "objects": true}},
		{"head-refs", map[string]bool{"HEAD": true, "refs": true}},
		{"objects-refs", map[string]bool{"objects": true, "refs": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			git := filepath.Join(dir, ".git")
			for name := range full {
				p := filepath.Join(git, name)
				if tc.keep[name] {
					if name == "HEAD" {
						if err := os.MkdirAll(git, 0o755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(p, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
							t.Fatal(err)
						}
					} else if err := os.MkdirAll(p, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, ok, certain := gitRootFast(dir); ok || certain {
				t.Errorf("gitRootFast accepted a .git with only %v", tc.keep)
			}
		})
	}
}

func TestGitRootFastDefersSymlinkedGit(t *testing.T) {
	// A `.git` that is itself a symlink, and a HEAD that is a
	// symlink, both defer: git follows links with its own rules, so
	// only the subprocess knows the verdict.
	dir := t.TempDir()
	real := filepath.Join(dir, "real-git")
	if err := os.MkdirAll(filepath.Join(real, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(real, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, ".git")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, ok, certain := gitRootFast(dir); ok || certain {
		t.Error("gitRootFast must defer a symlinked .git to the subprocess")
	}
	assertParity(t, dir)

	dir2 := t.TempDir()
	git2 := filepath.Join(dir2, ".git")
	if err := os.MkdirAll(filepath.Join(git2, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(git2, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	headTarget := filepath.Join(git2, "HEAD.target")
	if err := os.WriteFile(headTarget, []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(headTarget, filepath.Join(git2, "HEAD")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, ok, certain := gitRootFast(dir2); ok || certain {
		t.Error("gitRootFast must defer a symlinked HEAD to the subprocess")
	}
	assertParity(t, dir2)
}

func TestGitRootFastRejectsGarbageGitfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitlink\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Conservative fallback: the fast path must not claim this.
	if _, ok, certain := gitRootFast(dir); ok || certain {
		t.Error("gitRootFast accepted a garbage .git file")
	}
	assertParity(t, dir)
}

func TestGitRootFastHonorsGitEnv(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)
	for _, kv := range [][2]string{
		{"GIT_DIR", repoRoot},
		{"GIT_WORK_TREE", repoRoot},
		{"GIT_CEILING_DIRECTORIES", repoRoot},
		{"GIT_DISCOVERY_ACROSS_FILESYSTEM", "0"},
		{"GIT_COMMON_DIR", repoRoot},
	} {
		t.Setenv(kv[0], kv[1])
		if _, ok, certain := gitRootFast(repoRoot); ok || certain {
			t.Errorf("gitRootFast proceeded with %s set", kv[0])
		}
		assertParity(t, repoRoot)
	}
}

func TestGitRootParityWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)
	git := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		)
		return cmd.Run()
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git("add", "f.txt"); err != nil {
		t.Skipf("git add: %v", err)
	}
	if err := git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"); err != nil {
		t.Skipf("git commit: %v", err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := git("worktree", "add", "--detach", wt); err != nil {
		t.Skipf("git worktree add: %v", err)
	}
	// A worktree root carries a `.git` link file. Link acceptance
	// rules live in git internals (a gitfile pointing at a plain repo
	// is rejected while worktree/submodule links are honored), so
	// `.git` files always defer to the subprocess. Parity (not
	// engagement) is the assertion here; worktree checkouts keep
	// today's behavior exactly.
	assertParity(t, wt)
	wtSub := filepath.Join(wt, "sub")
	if err := os.MkdirAll(wtSub, 0o755); err != nil {
		t.Fatal(err)
	}
	assertParity(t, wtSub)
	if _, ok, certain := gitRootFast(wt); ok || certain {
		t.Error("gitRootFast must defer worktree links to the subprocess")
	}
}

func TestGitRootFastDefersGitfileWithoutBinary(t *testing.T) {
	// Even a well-formed worktree-style link (gitdir + commondir)
	// defers: acceptance rules live in git, not here.
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin")
	if err := os.MkdirAll(admin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"HEAD", "commondir", "gitlink"} {
		if err := os.WriteFile(filepath.Join(admin, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: ./admin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, certain := gitRootFast(dir); ok || certain {
		t.Error("gitRootFast must defer .git link files to the subprocess")
	}
}

func TestGitRootParitySubmodule(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	src := t.TempDir()
	initGitRepo(t, src)
	main := t.TempDir()
	initGitRepo(t, main)
	git := func(dir string, args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		)
		return cmd.Run()
	}
	mkcommit := func(dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := git(dir, "add", "f.txt"); err != nil {
			t.Skipf("git add: %v", err)
		}
		if err := git(dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"); err != nil {
			t.Skipf("git commit: %v", err)
		}
	}
	mkcommit(src)
	mkcommit(main)
	if err := git(main, "-c", "protocol.file.allow=always", "submodule", "-q", "add", src, "sub"); err != nil {
		t.Skipf("git submodule add: %v", err)
	}
	// Submodule roots carry `.git` link files; like worktrees they
	// defer to the subprocess, with parity as the assertion.
	sub := filepath.Join(main, "sub")
	assertParity(t, sub)
	if _, ok, certain := gitRootFast(sub); ok || certain {
		t.Error("gitRootFast must defer submodule links to the subprocess")
	}
}

func TestGitRootParityDotDotPath(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)
	sub := filepath.Join(repoRoot, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Lexical `..` in the input must not confuse discovery.
	assertParity(t, filepath.Join(sub, "..", "b"))
	assertParity(t, filepath.Join(sub, "..", "..", "a", "b"))
}

func TestGitRootParitySymlinkedSubdir(t *testing.T) {
	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)
	real := filepath.Join(repoRoot, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repoRoot, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	assertParity(t, link)
}

func TestGitRootFastEmptyGitfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, certain := gitRootFast(dir); ok || certain {
		t.Error("gitRootFast accepted an empty .git file")
	}
}

func TestGitRootFastOversizedGitfile(t *testing.T) {
	dir := t.TempDir()
	body := "gitdir: " + strings.Repeat("x", 64*1024) + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, certain := gitRootFast(dir); ok || certain {
		t.Error("gitRootFast accepted an oversized .git file")
	}
}
