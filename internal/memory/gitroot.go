package memory

import (
	"os"
	"path/filepath"
)

// gitRootSpawner runs the historical subprocess implementation. It is
// a package var (like other subprocess seams in this codebase) so
// tests can prove the fast path answers without spawning.
var gitRootSpawner = gitRootSpawn

// gitRootFast resolves the repository root for dir without spawning git,
// by walking upward for a `.git` entry — the same discovery `git
// rev-parse --show-toplevel` performs. Spawning git costs ~75ms on
// Windows, and this lookup runs on every startup, so the pure-Go path
// matters. It returns a DEFINITIVE answer in the clear cases and
// ok=false on any ambiguity, in which case the caller falls back to the
// subprocess (today's exact behavior). The fast path can therefore only
// ever agree with git, never contradict it:
//
// Definitive hits (ok=true):
//   - `.git` directory, same-owner (see gitRootOwnerMatches), with a
//     usable git dir (HEAD file plus objects/ and refs/ directories,
//     matching what `git rev-parse` requires): rev-parse succeeds with
//     this root.
//
// Definitive miss (ok=false, no spawn needed):
//   - no `.git` found up to the filesystem root: rev-parse fails too
//     (bare repos have no work tree, ceilings/non-repos fail the
//     same way).
//
// Ambiguous (ok=false, subprocess decides):
//   - GIT_DIR, GIT_WORK_TREE, GIT_CEILING_DIRECTORIES,
//     GIT_DISCOVERY_ACROSS_FILESYSTEM, or GIT_COMMON_DIR set: git's
//     discovery or worktree resolution differs.
//   - dir missing, not a directory, or unresolvable: git cannot chdir
//     there either.
//   - Lstat/permission/IO errors anywhere: cannot reason.
//   - volume/device change while walking up: git stops at filesystem
//     boundaries by default.
//   - cross-owner `.git` (Unix): git may refuse it as dubiously owned.
//     (Windows: the realistic same-user case is identical;
//     cross-user checkouts are documented on gitRootOwnerMatches.)
//   - unusable `.git` dir, `.git` files (worktree/submodule links),
//     symlinked or exotic `.git`: worktree link validation would
//     reimplement git internals (core.worktree vs commondir rules
//     differ observably), so links always defer to the subprocess.
//     Worktree checkouts keep today's behavior exactly.
//
// Roots are spelled exactly like rev-parse's output (forward slashes
// on Windows via gitRootSpelling) so project identity hashes never
// change. The starting directory is canonicalized first so discovery
// itself runs on the physical path, exactly like git's getcwd-based
// discovery (covers symlinked cwd components, junctions, and subst
// drives).
//
// The third result reports certainty: ok=true always implies
// certain=true; certain=true with ok=false means the walk exhausted
// the ancestor chain finding nothing, so rev-parse fails identically
// and no subprocess is needed. certain=false means ambiguity — the
// caller must spawn git (today's exact behavior).
func gitRootFast(dir string) (root string, ok, certain bool) {
	uncertain := func() (string, bool, bool) { return "", false, false }
	if os.Getenv("GIT_DIR") != "" ||
		os.Getenv("GIT_WORK_TREE") != "" ||
		os.Getenv("GIT_CEILING_DIRECTORIES") != "" ||
		os.Getenv("GIT_DISCOVERY_ACROSS_FILESYSTEM") != "" ||
		os.Getenv("GIT_COMMON_DIR") != "" {
		return uncertain()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return uncertain()
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		// git would fail to chdir here too; let the subprocess
		// produce the outcome (failure, like today).
		return uncertain()
	}
	// Physical discovery base: without this, a symlinked cwd (or a
	// junction/subst in the path) would search the wrong ancestor
	// chain and wrongly conclude "not a repo".
	if eval, err := filepath.EvalSymlinks(abs); err == nil && eval != "" {
		abs = eval
	} else if err != nil {
		return uncertain()
	}
	for d := abs; ; {
		gitPath := filepath.Join(d, ".git")
		fi, err := os.Lstat(gitPath)
		if err == nil {
			if fi.IsDir() {
				if !gitRootOwnerMatches(gitPath) || !gitDirUsable(gitPath) {
					return uncertain()
				}
				if r, ok := canonicalRoot(d); ok {
					return r, true, true
				}
				return uncertain()
			}
			// `.git` files (worktree/submodule links), symlinks, and
			// anything exotic defer to the subprocess; see above.
			return uncertain()
		} else if !os.IsNotExist(err) {
			// Permission or IO error: cannot reason; fall back.
			return uncertain()
		}
		parent := filepath.Dir(d)
		if parent == d {
			// Exhausted without finding `.git`: rev-parse fails
			// identically (non-repo, ceiling, or bare work tree),
			// so the miss is definitive and no spawn is needed.
			return "", false, true
		}
		if !sameVolume(d, parent) {
			// git stops discovery at filesystem boundaries by
			// default; only the subprocess knows its verdict here.
			return uncertain()
		}
		d = parent
	}
}

// gitDirUsable reports whether gitDir is a git directory rev-parse
// accepts: a regular HEAD file plus objects/ and refs/ directories,
// verified empirically (a `.git` with only HEAD is rejected by git
// as "not a git repository"). Being stricter than git only ever
// converts a hit into a subprocess fallback, never into a wrong
// answer: the fallback produces git's own verdict.
func gitDirUsable(gitDir string) bool {
	if fi, err := os.Lstat(filepath.Join(gitDir, "HEAD")); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if fi, err := os.Lstat(filepath.Join(gitDir, "objects")); err != nil || !fi.IsDir() {
		return false
	}
	if fi, err := os.Lstat(filepath.Join(gitDir, "refs")); err != nil || !fi.IsDir() {
		return false
	}
	return true
}

// canonicalRoot returns d spelled exactly like rev-parse prints it:
// symlinks resolved (physical path) with forward slashes on Windows
// (see gitRootSpelling). Resolution failure falls back to the
// subprocess rather than risking a differently-spelled root, since
// the spelling feeds project identity hashes.
func canonicalRoot(d string) (string, bool) {
	eval, err := filepath.EvalSymlinks(d)
	if err != nil || eval == "" {
		return "", false
	}
	return gitRootSpelling(eval), true
}
