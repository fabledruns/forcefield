//go:build linux

package landlock

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// maxKnownABI bounds the rights the spike requests: the base v1 set
// plus REFER (ABI 2) plus TRUNCATE (ABI 3). Newer kernels accept this
// older subset without error; the spike deliberately does not request
// network scopes, device ioctls, or signal scopes (P1-H/P2 scope).
const maxKnownABI = 3

// handledAccessForABI returns the filesystem rights mask the spike puts
// in handled_access_fs for the given ABI. It is pure so the ABI/rights
// matrix is unit-testable without a kernel:
//
//   - ABI 1 (5.13): base rights EXECUTE..MAKE_SYM. Cross-directory
//     rename/link (REFER) is denied by the kernel by default on ABI 1,
//     even unhandled — tool operations that reparent files across
//     directories fail there regardless of our rules.
//   - ABI 2 (5.19): +REFER, so cross-directory rename/link becomes
//     grantable beneath allowed paths.
//   - ABI 3 (6.2): +TRUNCATE. On older ABIs truncation cannot be
//     denied, but outside paths are still unreachable because opening
//     them for write is denied via WRITE_FILE.
//
// Future ABIs reuse the v3 mask: the spike requests only rights it
// understands, which newer kernels accept.
//
// SECURITY NOTE (spike-accepted fail-open): unhandled rights are
// allowed by Landlock semantics, so a newer kernel's additional
// rights (device ioctls, scopes, network) stay permitted under this
// mask. Correct for a filesystem-only spike; the production backend
// must revisit the mask per ABI instead of copying this table.
func handledAccessForABI(abi int) uint64 {
	const base = (uint64(1) << 13) - 1 // ABI v1 rights EXECUTE..MAKE_SYM
	mask := base
	if abi >= 2 {
		mask |= uint64(unix.LANDLOCK_ACCESS_FS_REFER)
	}
	if abi >= 3 {
		mask |= uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	}
	return mask
}

// readOnlyAccess is the grant for ReadOnly rules: read, list, and
// traverse/execute. No write, truncate, remove, create, or reparent
// rights cross into read-only hierarchies.
func readOnlyAccess() uint64 {
	return uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_EXECUTE)
}

// QueryABI asks the kernel which Landlock ABI it speaks via the
// documented version-query operation. It applies nothing.
func QueryABI() (int, error) {
	r1, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0, 0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return -1, fmt.Errorf("landlock spike: ABI query: %w", errno)
	}
	return int(r1), nil
}

// SetNoNewPrivs sets PR_SET_NO_NEW_PRIVS on the calling thread so the
// confined target (and anything it executes) cannot gain new
// privileges via setuid binaries or file capabilities.
func SetNoNewPrivs() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("landlock spike: no_new_privs: %w", err)
	}
	return nil
}

// createRulesetRaw creates a Landlock ruleset handling the given FS
// rights. A variable (not a plain func) so tests can stub ruleset
// creation failure after a successful ABI probe — the exact seam the
// fail-closed design needs.
var createRulesetRaw = func(handled uint64) (int, error) {
	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	r1, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)),
		unsafe.Sizeof(attr),
		0,
	)
	if errno != 0 {
		return -1, fmt.Errorf("landlock spike: create ruleset: %w", errno)
	}
	return int(r1), nil
}

// addPathRule grants allowed access beneath path in the ruleset. The
// path is opened O_PATH|O_CLOEXEC without O_NOFOLLOW, so a symlinked
// rule path grants its target: production callers must canonicalize
// rule paths before installing. No content is read, and the descriptor
// is closed before returning (rules persist in the ruleset, not on
// the fd).
func addPathRule(rulesetFd int, path string, allowed uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("landlock spike: open rule path %s: %w", path, err)
	}
	defer unix.Close(fd)
	attr := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(fd)}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFd),
		uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
		uintptr(unsafe.Pointer(&attr)),
		0, 0, 0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock spike: add rule for %s: %w", path, errno)
	}
	return nil
}

// restrictSelf applies the ruleset to the calling thread. Restrictions
// are inherited across fork and exec, which is the property the
// production helper design relies on (helper restricts, then execs
// the shell).
func restrictSelf(rulesetFd int) error {
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFd), 0, 0)
	if errno != 0 {
		return fmt.Errorf("landlock spike: restrict self: %w", errno)
	}
	return nil
}

// InstallRules creates a ruleset for the ABI-masked rights, grants
// every policy rule beneath its path, and restricts the caller. It
// must run on the thread that will execute the confined target (call
// runtime.LockOSThread first): Landlock attaches to the calling
// thread, and locking alone proves nothing — the observed denials in
// the spike tests are the proof.
func InstallRules(p Policy) error {
	abi, err := QueryABI()
	if err != nil {
		return err
	}
	if abi < 1 {
		return fmt.Errorf("landlock spike: impossible ABI %d", abi)
	}
	if abi > maxKnownABI {
		abi = maxKnownABI
	}
	handled := handledAccessForABI(abi)
	fd, err := createRulesetRaw(handled)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	for _, rule := range p.Allowed {
		allowed := handled
		switch {
		case rule.Access != 0:
			// Narrow grant, masked to what the ruleset handles:
			// rights outside the handled set would fail the
			// add_rule call (EINVAL) instead of widening access.
			allowed = rule.Access & handled
		case rule.ReadOnly:
			allowed = readOnlyAccess()
		}
		if err := addPathRule(fd, rule.Path, allowed); err != nil {
			return err
		}
	}
	if err := restrictSelf(fd); err != nil {
		return err
	}
	return nil
}

// ApplyPolicy is the full helper sequence: lock the OS thread, set
// no_new_privs, install the rules. It never executes a target itself;
// the caller execs afterwards on the same (locked) thread.
func ApplyPolicy(p Policy) error {
	runtime.LockOSThread()
	if p.NoNewPrivs {
		if err := SetNoNewPrivs(); err != nil {
			return err
		}
	}
	return InstallRules(p)
}
