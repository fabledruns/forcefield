//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"forcefield/internal/sandbox/landlock"
)

// corpusRODirs is the read-only system set the compatibility corpus
// proved sufficient for shell and Git workflows: user database, shell
// and tool binaries, shared libraries, git system config, TLS trust
// store, and device nodes. Entries that do not exist are skipped, not
// fabricated. /proc is deliberately absent (nothing tested needs it;
// granting it would expose /proc/<ppid>/environ), as is $HOME.
var corpusRODirs = []string{"/usr", "/bin", "/lib", "/lib64", "/sbin", "/etc", "/dev"}

// linuxExecutor runs commands under a Landlock filesystem ruleset plus
// no_new_privs via a re-executed helper (see HelperMain). Workspace
// and private tmp are read-write; the corpus system set is read-only;
// /dev/null gets a narrow read+write file grant (git opens it O_RDWR).
// Everything else is denied by the kernel. See docs/Sandbox.md.
type linuxExecutor struct {
	policy Policy

	mu            sync.Mutex
	supportProbed bool
	supportABI    int
	supportErr    error
}

// newIsolatedExecutor builds the isolated backend. The policy is
// validated by NewExecutor before this is reached.
func newIsolatedExecutor(p Policy) (Executor, error) {
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid sandbox policy: %w", err)
	}
	return &linuxExecutor{policy: p}, nil
}

// landlockABIFunc queries the kernel Landlock ABI. A variable (not a
// plain call) so tests can simulate unsupported kernels without one,
// mirroring the repo's seam style.
var landlockABIFunc = landlock.QueryABI

// support reports the cached Landlock ABI, probing once like the WSL
// backend probes once. An error means isolation cannot be established
// on this kernel; callers fail closed, never downgrade.
func (e *linuxExecutor) support() (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.supportProbed {
		e.supportABI, e.supportErr = landlockABIFunc()
		e.supportProbed = true
	}
	return e.supportABI, e.supportErr
}

// landlockDiagnostic formats an actionable error when Landlock is unavailable
// or unsupported on the running Linux kernel. It preserves ErrBackendUnavailable
// wrapping and fail-closed behavior while explaining how to verify kernel
// support and how to configure standard execution when isolation is unavailable.
func landlockDiagnostic(err error, abi int) error {
	var detail string
	if err != nil {
		detail = fmt.Sprintf("landlock unavailable: %v", err)
	} else if abi < 1 {
		detail = fmt.Sprintf("landlock reported unsupported ABI %d", abi)
	} else {
		detail = "landlock unsupported"
	}
	return fmt.Errorf("%w: %s (sandbox.mode = \"isolated\" requires a Landlock-capable Linux kernel [Linux 5.13+ with CONFIG_SECURITY_LANDLOCK=y and 'landlock' in lsm]; verify support via 'cat /sys/kernel/security/lsm' or 'grep LANDLOCK /boot/config-$(uname -r)'; if kernel-level isolation is unavailable, configure sandbox.mode = \"standard\")", ErrBackendUnavailable, detail)
}

// Probe verifies the isolated backend is usable right now: Bash
// present, Landlock available, and every configured extra path
// resolvable. Anything else refuses to run.
func (e *linuxExecutor) Probe(ctx context.Context) error {
	if _, err := bashLookPath("bash"); err != nil {
		return fmt.Errorf("bash was not found on PATH; Forcefield requires Bash for shell commands")
	}
	if abi, err := e.support(); err != nil || abi < 1 {
		return landlockDiagnostic(err, abi)
	}
	// Misconfigured extras fail here (startup/doctor) rather than
	// per-command inside the helper.
	for _, extra := range e.policy.FSRead {
		if _, err := canonicalRulePath(extra); err != nil {
			return fmt.Errorf("%w: sandbox.isolated read path: %v", ErrBackendUnavailable, err)
		}
	}
	for _, extra := range e.policy.FSWrite {
		if _, err := canonicalRulePath(extra); err != nil {
			return fmt.Errorf("%w: sandbox.isolated write path: %v", ErrBackendUnavailable, err)
		}
	}
	return nil
}

// canonicalRulePath resolves a rule path to its canonical absolute
// form (symlinks included) so rules pin the real location, never the
// spelling. Missing paths are an error: a rule that cannot be opened
// must fail closed at Prepare time, not silently cover nothing.
func canonicalRulePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty rule path", ErrInvalidDir)
	}
	abs := path
	if !filepath.IsAbs(abs) && !isAbsLike(abs) {
		join, err := filepath.Abs(abs)
		if err != nil {
			return "", fmt.Errorf("resolve rule path %s: %w", path, err)
		}
		abs = join
	}
	resolved, err := EvalLinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidDir, abs)
	}
	return resolved, nil
}

// isolatedRules builds the Landlock allow rules for one execution:
// read-write workspace and private tmp, the read-only corpus system
// set (existing entries only), a narrow read+write grant on /dev/null,
// and the policy's explicit extras (read-only or writable). Extras are
// canonicalized and must exist; anything else fails closed. An extra
// naming a built-in path overrides the built-in grant: explicit
// configuration wins, and the override is visible in the rule list
// (doctor renders the count, not the paths).
func isolatedRules(workspaceRoot, tmpDir string, p Policy) ([]landlock.Rule, error) {
	rules := []landlock.Rule{{Path: workspaceRoot}, {Path: tmpDir}}
	index := map[string]int{workspaceRoot: 0, tmpDir: 1}
	set := func(path string, readOnly bool, access uint64) {
		if i, ok := index[path]; ok {
			rules[i] = landlock.Rule{Path: path, ReadOnly: readOnly, Access: access}
			return
		}
		index[path] = len(rules)
		rules = append(rules, landlock.Rule{Path: path, ReadOnly: readOnly, Access: access})
	}
	addRO := func(path string) {
		resolved, err := EvalLinks(path)
		if err != nil {
			return
		}
		set(resolved, true, 0)
	}
	for _, dir := range corpusRODirs {
		addRO(dir)
	}
	if _, err := os.Stat("/dev/null"); err == nil {
		set("/dev/null", false, uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_WRITE_FILE))
	}
	for _, extra := range p.FSRead {
		resolved, err := canonicalRulePath(extra)
		if err != nil {
			return nil, fmt.Errorf("sandbox.isolated read path: %w", err)
		}
		set(resolved, true, 0)
	}
	for _, extra := range p.FSWrite {
		resolved, err := canonicalRulePath(extra)
		if err != nil {
			return nil, fmt.Errorf("sandbox.isolated write path: %w", err)
		}
		set(resolved, false, 0)
	}
	return rules, nil
}

// Prepare validates req against the policy and returns a
// ready-to-start helper command. The working directory is always
// pinned to the workspace in isolated mode. It never starts the
// process and never falls back to another backend.
func (e *linuxExecutor) Prepare(ctx context.Context, req Request) (*Prepared, error) {
	if err := e.policy.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	if abi, err := e.support(); err != nil || abi < 1 {
		return nil, landlockDiagnostic(err, abi)
	}
	dir, err := resolveWithinWorkspace(e.policy.Workspace, req.Dir)
	if err != nil {
		return nil, err
	}
	wsRoot, err := resolveWithinWorkspace(e.policy.Workspace, "")
	if err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp("", "ff-isolated-*")
	if err != nil {
		return nil, fmt.Errorf("create isolated temp directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	// Canonicalize for the rule: /tmp itself may be a symlink, and the
	// rule must pin the real location.
	if resolved, err := EvalLinks(tmpDir); err == nil {
		tmpDir = resolved
	}
	bash, err := bashLookPath("bash")
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: bash was not found on PATH", ErrBackendUnavailable)
	}
	// The interpreter must stay executable under confinement: grant
	// its directory read-only alongside the explicit extras, so a
	// bash outside the built-in set (nix, /opt) works instead of
	// failing obscurely inside the helper. Unresolvable here refuses
	// up front, naming the interpreter rather than a rule path.
	bashDir, err := canonicalRulePath(filepath.Dir(bash))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: resolve interpreter directory: %v", ErrBackendUnavailable, err)
	}
	pol := e.policy
	pol.FSRead = append([]string{bashDir}, pol.FSRead...)
	rules, err := isolatedRules(wsRoot, tmpDir, pol)
	if err != nil {
		cleanup()
		return nil, err
	}
	// TMPDIR, GIT_CONFIG_GLOBAL, and GIT_CONFIG_NOSYSTEM are filtered from
	// both inheritance and explicit extras before the isolated security
	// defaults are appended: syscall.Exec performs no deduplication (unlike
	// exec.Cmd), and libc getenv honors the first match, so duplicates would
	// make the winning value depend on slice order.
	//
	// Precedence: Isolated sandbox security defaults strictly take precedence
	// over both host environment variables and caller-supplied ExtraEnv.
	// - TMPDIR is pinned to the isolated private temp directory.
	// - GIT_CONFIG_GLOBAL is pinned to /dev/null so git commands cannot
	//   attempt to read ~/.gitconfig (which is denied by Landlock and causes exit 128).
	// - GIT_CONFIG_NOSYSTEM is pinned to 1 so git does not probe /etc/gitconfig.
	stripVars := append(append([]string(nil), isolatedSecurityEnvVars...), e.policy.CredentialEnv...)
	env := append(stripCredentialEnv(os.Environ(), stripVars), stripIsolatedExtras(req.ExtraEnv)...)
	env = append(env,
		"TMPDIR="+tmpDir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	helperReq := helperRequest{
		Version:    helperVersion,
		Command:    req.Command,
		Dir:        dir,
		Env:        env,
		Bash:       bash,
		Rules:      rules,
		NoNewPrivs: true,
	}
	policyReader, policyWriter, err := os.Pipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("create helper policy pipe: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		_ = policyReader.Close()
		_ = policyWriter.Close()
		cleanup()
		return nil, fmt.Errorf("%w: resolve helper binary: %v", ErrBackendUnavailable, err)
	}
	cmd := exec.CommandContext(ctx, exe, HelperArg)
	cmd.Dir = dir
	cmd.ExtraFiles = []*os.File{policyReader}
	// Stage the request in a goroutine, not inline: the pipe reader
	// only exists once the caller Starts the command (lifecycle stays
	// with the caller per the Executor contract), so a synchronous
	// write of a large command or environment could deadlock on the
	// pipe buffer with no reader and no ctx cancellation possible.
	// The writer closes when done; Cleanup closes it as well, which
	// unblocks this goroutine if the command is never started.
	// Oversized requests still fail, but closed: the helper refuses
	// past its read cap with exit 126 instead of hanging.
	go func() {
		_ = writeHelperRequest(policyWriter, helperReq)
		_ = policyWriter.Close()
	}()
	cleanups := func() {
		_ = policyReader.Close()
		_ = policyWriter.Close()
		cleanup()
	}
	return &Prepared{Cmd: cmd, Dir: dir, Cleanup: cleanups}, nil
}

// isolatedSecurityEnvVars lists environment variable names reserved and
// enforced by the isolated sandbox backend.
//
// Precedence:
// The isolated sandbox security defaults strictly take precedence over both
// inherited host environment variables and caller-supplied Request.ExtraEnv.
// Neither host inheritance nor explicit ExtraEnv can override or duplicate
// these variables:
//   - TMPDIR: always points to the isolated private temporary directory.
//   - GIT_CONFIG_GLOBAL: pinned to /dev/null to ensure git commands do not
//     attempt to read $HOME/.gitconfig. Under Landlock confinement, $HOME is
//     intentionally excluded from the sandbox's read-only corpus; unhandled
//     reads fail with EACCES and cause git to exit with fatal error code 128.
//   - GIT_CONFIG_NOSYSTEM: pinned to 1 so git does not probe /etc/gitconfig.
var isolatedSecurityEnvVars = []string{
	"TMPDIR",
	"GIT_CONFIG_GLOBAL",
	"GIT_CONFIG_NOSYSTEM",
}

func isIsolatedReservedEnv(name string) bool {
	for _, reserved := range isolatedSecurityEnvVars {
		if strings.EqualFold(name, reserved) {
			return true
		}
	}
	return false
}

// stripIsolatedExtras drops explicit per-command entries for variables
// whose values are strictly controlled by the isolated sandbox:
// TMPDIR (always points to the private temp dir), GIT_CONFIG_GLOBAL
// (always /dev/null), and GIT_CONFIG_NOSYSTEM (always 1). Explicit per-command
// extras cannot override or weaken these security defaults.
func stripIsolatedExtras(extra []string) []string {
	out := extra[:0:0]
	for _, kv := range extra {
		if name, _, _ := strings.Cut(kv, "="); !isIsolatedReservedEnv(name) {
			out = append(out, kv)
		}
	}
	return out
}

// stripTMPDIRExtras is preserved for backwards compatibility with any
// callers expecting the previous helper name.
func stripTMPDIRExtras(extra []string) []string {
	return stripIsolatedExtras(extra)
}

// Describe reports what the isolated backend enforces. Filesystem
// confinement is claimed only when Landlock support probed OK;
// otherwise the same structured ID warns and commands refuse to run.
func (e *linuxExecutor) Describe(context.Context) Enforcement {
	abi, err := e.support()
	confined := err == nil
	limitations := []Limitation{
		{ID: LimFilesystemToolsCaged, Detail: "filesystem tools are confined to the project workspace via tool-layer policy"},
		{ID: LimShellTextConfined, Detail: "shell command text is confined by Landlock: writes inside workspace and private tmp, reads add system paths read-only"},
		{ID: LimNetworkNamespace, Detail: "host networking; no isolation is attempted in isolated mode"},
		{ID: LimEnvFullHost, Detail: "host environment is forwarded by design"},
	}
	if confined {
		limitations = append(limitations, Limitation{
			ID:     LimFilesystemLandlock,
			Detail: fmt.Sprintf("Landlock filesystem confinement enforced (ABI %d): workspace and private tmp read-write, system paths read-only, /proc and $HOME denied", abi),
		})
	} else {
		limitations = append(limitations, Limitation{
			ID:     LimFilesystemLandlock,
			Warn:   true,
			Detail: fmt.Sprintf("Landlock unavailable on this kernel (%v); commands refuse to run rather than run unconfined", err),
		})
	}
	limitations = append(limitations, credentialStrippedLimitation(e.policy, true)...)
	limitations = append(limitations, processLimitations()...)
	notes := []string{
		"isolated execution: Landlock filesystem confinement to the workspace; shell working directory is always pinned",
	}
	if confined {
		// The ABI rides in Notes (rendered by doctor and approval
		// UI) rather than only in the info limitation, which doctor
		// does not render: "doctor reports the ABI" must be true in
		// the output users actually see.
		notes = append(notes, fmt.Sprintf("Landlock ABI %d enforced; private temp directory always wins over TMPDIR", abi))
	}
	return Enforcement{
		Mode:               ModeIsolated,
		Network:            NetworkHost,
		CwdPinned:          true,
		FilesystemConfined: confined,
		EnvForwarded:       true,
		NetworkEnforced:    false,
		Notes:              notes,
		Limitations:        limitations,
	}
}

var _ Executor = (*linuxExecutor)(nil)
