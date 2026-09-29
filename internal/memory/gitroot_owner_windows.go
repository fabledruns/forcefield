//go:build windows

package memory

// gitRootOwnerMatches is the Windows half of the dubious-ownership
// guard (see gitroot_owner_unix.go). Reading a file's owner SID needs
// security-descriptor plumbing with no cheap stdlib path, so this
// always returns true: the realistic case — the user owns their own
// checkout — resolves identically to `git rev-parse`, which the parity
// tests pin. A repository owned by a different user (service accounts,
// runas, cross-user mounts) may resolve to the repo root where git
// itself would refuse; that residual divergence is confined to
// environments where the safe.directory policy is already in play, and
// the subprocess fallback still covers every other ambiguity
// (GIT_* env, unreadable paths, broken worktree links).
func gitRootOwnerMatches(gitPath string) bool {
	return true
}
