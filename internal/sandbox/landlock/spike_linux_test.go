//go:build linux

package landlock

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// spikeChildEnv marks the re-executed test binary as the confined
// helper under test. The parent tests below spawn
// `os.Args[0] -test.run=^TestLandlockSpikeChild$` with this set,
// following the repo's helper-process pattern (see
// mcp_helper_test.go): no production entry point changes, no shell,
// no scripts.
const spikeChildEnv = "FF_LANDLOCK_SPIKE_CHILD"

// spikeDeniedEnv carries the denied-directory coordinate to the child.
// The versioned policy pipe carries only allow rules; test coordinates
// travel out of band so the production-shaped contract stays clean.
const spikeDeniedEnv = "FF_LANDLOCK_SPIKE_DENIED"

// spikeApplyEnv selects the ApplyPolicy composition in the child (see
// runSpikeChild): when "1", the child runs the production-shaped entry
// instead of the manual lock/NNP/install sequence.
const spikeApplyEnv = "FF_LANDLOCK_SPIKE_APPLY"

// spikeShellEnv selects shell-script mode in the child (see
// runSpikeShell): when "1", the child confines itself and runs the
// script from spikeScriptEnv with cwd spikeWorkdirEnv instead of the
// fixed filesystem checks.
const spikeShellEnv = "FF_LANDLOCK_SPIKE_SHELL"

// spikeScriptEnv carries the bash script for shell mode.
const spikeScriptEnv = "FF_LANDLOCK_SPIKE_SCRIPT"

// spikeWorkdirEnv carries the working directory for shell mode.
const spikeWorkdirEnv = "FF_LANDLOCK_SPIKE_WS"

const (
	spikeAllowedContent = "spike-allowed-marker"
	spikeDeniedContent  = "spike-denied-marker"
)

// TestLandlockSpikeChild is the confined helper entrypoint. It runs
// meaningfully only as a re-executed child (see above); a direct run
// skips. It locks its OS thread, validates the piped policy, sets
// no_new_privs, installs the ruleset, and then proves enforcement with
// direct file operations plus an exec-inherited grandchild probe.
func TestLandlockSpikeChild(t *testing.T) {
	if os.Getenv(spikeChildEnv) != "1" {
		t.Skip("spike child entrypoint; run via the parent spike tests")
	}
	// The restriction must attach to the exact thread that runs the
	// checks and execs descendants. LockOSThread alone proves
	// nothing — the observed denials below are the proof.
	runtime.LockOSThread()
	if os.Getenv(spikeShellEnv) == "1" {
		os.Exit(runSpikeShell())
	}
	os.Exit(runSpikeChild())
}

// runSpikeShell executes one shell workflow script under confinement
// and reports the result envelope: "RUN-EXIT=<code>" plus capped
// script output, always with child exit 0. The PARENT decides whether
// the outcome is expected (per corpus case), so a denied operation
// surfaces as data, never as a transport error. Setup failures keep
// the 126 + FFSBX protocol.
func runSpikeShell() int {
	setupFail := func(kind string, err error) int {
		fmt.Fprintf(os.Stderr, "%s%s:%v\n", SentinelPrefix, kind, err)
		return ExitSetupFailed
	}
	pipe := os.NewFile(3, "spike-policy")
	policy, err := ReadPolicy(pipe)
	_ = pipe.Close()
	if err != nil {
		return setupFail("POLICY", err)
	}
	ws := os.Getenv(spikeWorkdirEnv)
	script := os.Getenv(spikeScriptEnv)
	if ws == "" || script == "" {
		return setupFail("POLICY", fmt.Errorf("missing workdir/script coordinates"))
	}
	// Resolve bash before restricting: PATH lookup needs filesystem
	// access the ruleset will remove.
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		return setupFail("SETUP", fmt.Errorf("bash probe missing: %w", err))
	}
	if err := setupRestricted(policy); err != nil {
		var ue unavailableError
		if errors.As(err, &ue) {
			return setupFail("UNAVAILABLE", err)
		}
		return setupFail("SETUP", err)
	}
	cmd := exec.Command(bashPath, "-c", script)
	cmd.Dir = ws
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			return setupFail("SETUP", fmt.Errorf("start confined shell: %w", err))
		}
	}
	if len(out) > 8<<10 {
		out = append(append([]byte(nil), out[:8<<10]...), []byte("\n[...capped]")...)
	}
	fmt.Fprintf(os.Stdout, "RUN-EXIT=%d\n%s\n", code, out)
	return 0
}

// runSpikeChild executes the confined scenario. Setup failures print
// "FFSBX:<kind>:<detail>" to stderr with exit 126; boundary-check
// failures print "SPIKE-FAIL:<what>" with exit 1; success prints
// "SPIKE-OK" with exit 0. Parents distinguish all three.
func runSpikeChild() int {
	setupFail := func(kind string, err error) int {
		fmt.Fprintf(os.Stderr, "%s%s:%v\n", SentinelPrefix, kind, err)
		return ExitSetupFailed
	}
	pipe := os.NewFile(3, "spike-policy")
	policy, err := ReadPolicy(pipe)
	_ = pipe.Close()
	if err != nil {
		return setupFail("POLICY", err)
	}
	deniedDir := os.Getenv(spikeDeniedEnv)
	if deniedDir == "" {
		return setupFail("POLICY", fmt.Errorf("missing denied-dir coordinate"))
	}
	// Resolve the grandchild probe before restricting: PATH lookup
	// needs filesystem access the ruleset will remove.
	catPath, catErr := exec.LookPath("cat")
	// FF_LANDLOCK_SPIKE_APPLY selects the production-shaped entry
	// (ApplyPolicy: lock, NNP, install) instead of the manual
	// sequence below, so both compositions run the same confined
	// checks. The manual path additionally verifies NNP positively
	// via /proc while it is still readable.
	if os.Getenv(spikeApplyEnv) == "1" {
		// Production-shaped entry (lock, NNP, install in one call).
		// Query first so environmental blockage still reports
		// UNAVAILABLE; past that, every failure is breakage.
		if _, err := QueryABI(); err != nil {
			return setupFail(unavailableKind(err), err)
		}
		if err := ApplyPolicy(policy); err != nil {
			return setupFail("SETUP", err)
		}
	} else {
		if err := setupRestricted(policy); err != nil {
			var ue unavailableError
			if errors.As(err, &ue) {
				return setupFail("UNAVAILABLE", err)
			}
			return setupFail("SETUP", err)
		}
	}

	allowedDir := policy.Allowed[0].Path
	checkFail := func(format string, args ...any) int {
		fmt.Fprintf(os.Stdout, "SPIKE-FAIL:"+format+"\n", args...)
		return 1
	}

	// Positive: allowed read, write, and re-read.
	allowedFile := filepath.Join(allowedDir, "canary.txt")
	data, err := os.ReadFile(allowedFile)
	if err != nil || string(data) != spikeAllowedContent {
		return checkFail("allowed read: data=%q err=%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(allowedDir, "fresh.txt"), []byte("x"), 0o600); err != nil {
		return checkFail("allowed write: %v", err)
	}

	// Negative: read, write, and symlink-escape into the denied dir.
	deniedFile := filepath.Join(deniedDir, "secret.txt")
	if data, err := os.ReadFile(deniedFile); err == nil {
		return checkFail("denied read succeeded: %q", data)
	} else if !errors.Is(err, unix.EACCES) {
		return checkFail("denied read failed with %v, want EACCES", err)
	}
	if err := os.WriteFile(filepath.Join(deniedDir, "nope.txt"), []byte("x"), 0o600); err == nil {
		return checkFail("denied write succeeded")
	}
	if data, err := os.ReadFile(filepath.Join(allowedDir, "escape-link")); err == nil {
		return checkFail("symlink escape read succeeded: %q", data)
	} else if !errors.Is(err, unix.EACCES) {
		return checkFail("symlink escape failed with %v, want EACCES", err)
	}

	// Sibling outside every allow rule: the confinement covers the
	// allowed subtree, not the whole temporary tree.
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(allowedDir), "outside.txt")); err == nil {
		return checkFail("sibling-outside read succeeded: %q", data)
	} else if !errors.Is(err, unix.EACCES) {
		return checkFail("sibling-outside read failed with %v, want EACCES", err)
	}

	// /proc was readable before InstallRules (NNP verification above);
	// with no /proc rule it must now be denied.
	if _, err := os.ReadFile("/proc/self/status"); err == nil {
		return checkFail("/proc read succeeded after restrict")
	} else if !errors.Is(err, unix.EACCES) {
		return checkFail("/proc read failed with %v, want EACCES", err)
	}

	// Directory and rename operations a shell workload needs: allowed
	// subtree permits them, the denied side refuses them.
	if err := os.Mkdir(filepath.Join(allowedDir, "newdir"), 0o755); err != nil {
		return checkFail("allowed mkdir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(deniedDir, "newdir"), 0o755); err == nil {
		return checkFail("denied mkdir succeeded")
	} else if !errors.Is(err, unix.EACCES) {
		return checkFail("denied mkdir failed with %v, want EACCES", err)
	}
	moveSrc := filepath.Join(allowedDir, "mv.txt")
	if err := os.Rename(moveSrc, filepath.Join(allowedDir, "mv2.txt")); err != nil {
		return checkFail("allowed same-dir rename: %v", err)
	}
	if err := os.Rename(filepath.Join(allowedDir, "mv2.txt"), filepath.Join(deniedDir, "mv3.txt")); err == nil {
		return checkFail("cross-dir rename into denied succeeded")
	} else if !errors.Is(err, unix.EACCES) {
		return checkFail("cross-dir rename failed with %v, want EACCES", err)
	}
	if err := os.Remove(filepath.Join(allowedDir, "mv2.txt")); err != nil {
		return checkFail("allowed unlink: %v", err)
	}

	// Exec inheritance: the restriction must survive into a
	// grandchild, which is the property the production helper design
	// relies on (restrict, then exec the shell).
	if catErr != nil {
		return checkFail("cat probe unavailable: %v", catErr)
	}
	if out, err := exec.Command(catPath, allowedFile).CombinedOutput(); err != nil || string(out) != spikeAllowedContent {
		return checkFail("grandchild allowed read: out=%q err=%v", out, err)
	}
	if out, err := exec.Command(catPath, deniedFile).CombinedOutput(); err == nil {
		return checkFail("grandchild denied read succeeded: %q", out)
	} else if strings.Contains(string(out), spikeDeniedContent) {
		return checkFail("grandchild denied read leaked content: %q", out)
	}

	abi, _ := QueryABI()
	fmt.Fprintf(os.Stdout, "SPIKE-OK abi=%d\n", abi)
	return 0
}

func verifyNoNewPrivs() error {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Errorf("read nnp status: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "NoNewPrivs:") {
			if strings.TrimSpace(strings.TrimPrefix(line, "NoNewPrivs:")) == "1" {
				return nil
			}
			return fmt.Errorf("no_new_privs not set: %q", line)
		}
	}
	return fmt.Errorf("NoNewPrivs line missing in /proc/self/status")
}

// unavailableError marks a pre-install ABI-query failure as
// environmental blockage (skip with reason) as opposed to genuine
// setup breakage (fail). Past a successful query, every failure is
// breakage by definition.
type unavailableError struct{ err error }

func (e unavailableError) Error() string { return e.err.Error() }
func (e unavailableError) Unwrap() error { return e.err }

// setupRestricted applies the manual helper sequence on the calling
// (already locked) thread: no_new_privs with positive verification,
// environmental classification of query-phase blockage, then ruleset
// installation. It returns unavailableError for skip-worthy blockage
// and plain errors for breakage.
func setupRestricted(p Policy) error {
	if p.NoNewPrivs {
		if err := SetNoNewPrivs(); err != nil {
			return fmt.Errorf("NNP: %w", err)
		}
		// /proc is still unrestricted here; after InstallRules it may
		// not be, so verification happens now.
		if err := verifyNoNewPrivs(); err != nil {
			return fmt.Errorf("NNP: %w", err)
		}
	}
	if _, err := QueryABI(); err != nil {
		if isSpikeUnavailable(err) {
			return unavailableError{err}
		}
		return err
	}
	return InstallRules(p)
}

// isSpikeUnavailable reports environmental Landlock blockage (no
// syscall, LSM-disabled, container seccomp, or a ruleset-attr size the
// kernel rejects with E2BIG) as distinct from genuine setup breakage,
// so parents skip the former and fail the latter. E2BIG is treated
// conservatively as kernel incompatibility: an attr size the kernel
// will not accept means this kernel cannot run our rulesets, which is
// an environment property, not a programming bug.
func isSpikeUnavailable(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.E2BIG)
}

// unavailableKind selects the setup-failure sentinel for a
// pre-install ABI-query failure: environmental blockage reports
// UNAVAILABLE (parents skip with reason), anything else SETUP
// (parents fail). Callers must invoke it only before InstallRules;
// past a successful query, failures are breakage by definition.
func unavailableKind(err error) string {
	if isSpikeUnavailable(err) {
		return "UNAVAILABLE"
	}
	return "SETUP"
}

// requireSpikeABI skips the test when this kernel cannot do Landlock.
// Callers that need a working backend (not just the query) additionally
// branch on the child's UNAVAILABLE report.
func requireSpikeABI(t *testing.T) int {
	t.Helper()
	abi, err := QueryABI()
	if err != nil {
		t.Skipf("landlock unavailable, skipping spike execution: %v", err)
	}
	return abi
}

// standardRODirs lists FHS prefixes the grandchild probe may need
// (loader, libc, cat binary). /dev is required too: Go's os/exec opens
// /dev/null for a nil Stdin, so even a fully piped child needs it.
// The parent keeps only entries that exist; the child's allowed-cat
// success proves sufficiency empirically for that machine.
var standardRODirs = []string{"/usr", "/bin", "/lib", "/lib64", "/sbin", "/dev"}

// corpusRODirs is the candidate read-only system set under test:
// standardRODirs plus /etc (user database, git system config, TLS
// trust store). /proc is deliberately absent; the proc-denied case
// proves nothing tested needs it.
var corpusRODirs = []string{"/usr", "/bin", "/lib", "/lib64", "/sbin", "/etc", "/dev"}

func spikeFixture(t *testing.T) (allowedDir, deniedDir, catDir string) {
	t.Helper()
	root := t.TempDir()
	allowedDir = filepath.Join(root, "allowed")
	deniedDir = filepath.Join(root, "denied")
	for _, dir := range []string{allowedDir, deniedDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(allowedDir, "canary.txt"), []byte(spikeAllowedContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allowedDir, "mv.txt"), []byte("move-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deniedDir, "secret.txt"), []byte(spikeDeniedContent), 0o600); err != nil {
		t.Fatal(err)
	}
	// Sibling file outside every allow rule (parent of allowedDir).
	if err := os.WriteFile(filepath.Join(root, "outside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Outside-pointing symlink inside the allowed hierarchy: opening
	// through it must resolve outside the allow rule and be denied.
	if err := os.Symlink(filepath.Join(deniedDir, "secret.txt"), filepath.Join(allowedDir, "escape-link")); err != nil {
		t.Fatalf("create escape symlink: %v", err)
	}
	catPath, err := exec.LookPath("cat")
	if err != nil {
		t.Fatalf("cat probe missing (required for the exec-inheritance check): %v", err)
	}
	return allowedDir, deniedDir, filepath.Dir(catPath)
}

func spikePolicy(allowedDir, catDir string) Policy {
	rules := []Rule{{Path: allowedDir}}
	seen := map[string]bool{allowedDir: true}
	addRO := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			return
		}
		rules = append(rules, Rule{Path: path, ReadOnly: true})
	}
	addRO(catDir)
	for _, dir := range standardRODirs {
		addRO(dir)
	}
	return Policy{Version: PolicyVersion, NoNewPrivs: true, Allowed: rules}
}

// spawnSpikeChild writes policy to the child over an inherited pipe
// (the read end arrives as fd 3) and runs the re-executed test binary
// to completion, returning its exit code and combined output.
func spawnSpikeChild(t *testing.T, write func(w *os.File) error, extraEnv []string) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLandlockSpikeChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), spikeChildEnv+"=1")
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.ExtraFiles = []*os.File{r}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		t.Fatalf("child start: %v", err)
	}
	// The parent holds no copy of the read end past Start; the child
	// owns its duplicate. Closing here also lets a truncated write
	// surface as EOF in the child.
	_ = r.Close()
	writeErr := write(w)
	_ = w.Close()
	if writeErr != nil {
		_ = cmd.Wait()
		t.Fatalf("policy write: %v", writeErr)
	}
	waitErr := cmd.Wait()
	code := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("child wait: %v", waitErr)
		}
	}
	return code, stdout.String() + stderr.String()
}

// TestLandlockSpikeEnforcesBoundary is the feasibility proof: with a
// working Landlock kernel, the confined child exercises its allow
// rules (reads, writes, mkdir, rename, unlink, directly and via an
// exec-inherited grandchild) and is denied outside them, with
// no_new_privs verified.
func TestLandlockSpikeEnforcesBoundary(t *testing.T) {
	runSpikeScenario(t, nil)
}

// TestLandlockSpikeApplyPolicyPath runs the identical confined
// scenario through the production-shaped ApplyPolicy entry (lock, NNP,
// install) instead of the manual sequence, so a composition or
// ordering bug in ApplyPolicy fails a real confined execution rather
// than going uncovered.
func TestLandlockSpikeApplyPolicyPath(t *testing.T) {
	runSpikeScenario(t, []string{spikeApplyEnv + "=1"})
}

// runSpikeScenario builds the fixture, confines the child, and asserts
// the SPIKE-OK proof. extraEnv reaches the child verbatim (e.g. the
// ApplyPolicy mode selector above).
func runSpikeScenario(t *testing.T, extraEnv []string) {
	t.Helper()
	requireSpikeABI(t)
	allowedDir, deniedDir, catDir := spikeFixture(t)
	policy := spikePolicy(allowedDir, catDir)
	env := []string{spikeDeniedEnv + "=" + deniedDir}
	env = append(env, extraEnv...)
	code, combined := spawnSpikeChild(t, func(w *os.File) error {
		return WritePolicy(w, policy)
	}, env)
	if code != 0 {
		if strings.Contains(combined, SentinelPrefix+"UNAVAILABLE") {
			t.Skipf("landlock blocked in this environment: %s", firstLine(combined))
		}
		t.Fatalf("spike child exit %d, want 0 (SPIKE-OK):\n%s", code, combined)
	}
	if !strings.Contains(combined, "SPIKE-OK") {
		t.Fatalf("spike child exit 0 without SPIKE-OK:\n%s", combined)
	}
	for _, bad := range []string{"SPIKE-FAIL", SentinelPrefix} {
		if strings.Contains(combined, bad) {
			t.Fatalf("spike output contains failure marker %q:\n%s", bad, combined)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestLandlockSpikePolicyValidation feeds malformed transports to the
// child: every case must exit 126 with the FFSBX sentinel, without
// needing a Landlock-capable kernel (validation precedes syscalls).
func TestLandlockSpikePolicyValidation(t *testing.T) {
	withHeader := func(body []byte, length uint32) []byte {
		header := make([]byte, 8+4)
		copy(header, policyMagic)
		binary.BigEndian.PutUint32(header[8:], length)
		return append(header, body...)
	}
	cases := []struct {
		name    string
		payload func() []byte
	}{
		{"empty", func() []byte { return nil }},
		{"truncated-header", func() []byte { return []byte("FFSBX") }},
		{"bad-magic", func() []byte { return []byte("XXXXXXXX\x00\x00\x00\x01Z") }},
		{"oversized", func() []byte { return withHeader(nil, MaxPolicyBytes+1) }},
		{"truncated-body", func() []byte {
			return withHeader([]byte(`{"version":1`), 0x40)
		}},
		{"bad-version", func() []byte {
			return encodeTestPolicy(Policy{Version: 99, Allowed: []Rule{{Path: "/tmp/x"}}})
		}},
		{"unknown-field", func() []byte {
			return encodeTestPolicyRaw(`{"version":1,"no_new_privs":false,"allowed":[{"path":"/tmp/x","read_only":false}],"future":true}`)
		}},
		{"no-allowed", func() []byte {
			return encodeTestPolicy(Policy{Version: PolicyVersion})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := tc.payload()
			code, combined := spawnSpikeChild(t, func(w *os.File) error {
				_, err := w.Write(payload)
				return err
			}, []string{spikeDeniedEnv + "=" + t.TempDir()})
			if code != ExitSetupFailed {
				t.Fatalf("exit %d, want %d (setup failure):\n%s", code, ExitSetupFailed, combined)
			}
			if !strings.Contains(combined, SentinelPrefix) {
				t.Fatalf("missing %q sentinel in child output:\n%s", SentinelPrefix, combined)
			}
		})
	}
}

func encodeTestPolicy(p Policy) []byte {
	var b strings.Builder
	if err := WritePolicy(&b, p); err != nil {
		panic(err)
	}
	return []byte(b.String())
}

func encodeTestPolicyRaw(jsonBody string) []byte {
	header := make([]byte, 8+4)
	copy(header, policyMagic)
	binary.BigEndian.PutUint32(header[8:], uint32(len(jsonBody)))
	return append(header, jsonBody...)
}

// TestInstallRulesCreateFailure proves fail-closed wiring: ruleset
// creation failing after a successful ABI probe surfaces an error
// instead of an unconfined child.
func TestInstallRulesCreateFailure(t *testing.T) {
	requireSpikeABI(t)
	orig := createRulesetRaw
	t.Cleanup(func() { createRulesetRaw = orig })
	createRulesetRaw = func(uint64) (int, error) {
		return -1, unix.EINVAL
	}
	err := InstallRules(Policy{Version: PolicyVersion, Allowed: []Rule{{Path: t.TempDir()}}})
	if err == nil {
		t.Fatal("InstallRules succeeded despite ruleset creation failure; want an error")
	}
	// The stub's exact errno must propagate (not be swallowed or
	// replaced): fail-closed wiring depends on seeing the real cause.
	if !errors.Is(err, unix.EINVAL) {
		t.Errorf("error %v does not wrap the stubbed EINVAL", err)
	}
}

func TestHandledAccessForABI(t *testing.T) {
	base := (uint64(1) << 13) - 1
	v1 := handledAccessForABI(1)
	if v1 != base {
		t.Errorf("abi 1 mask = %#x, want base %#x", v1, base)
	}
	if v1&uint64(unix.LANDLOCK_ACCESS_FS_REFER) != 0 {
		t.Error("abi 1 mask must not contain REFER")
	}
	if v1&uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE) != 0 {
		t.Error("abi 1 mask must not contain TRUNCATE")
	}
	v2 := handledAccessForABI(2)
	if v2&uint64(unix.LANDLOCK_ACCESS_FS_REFER) == 0 {
		t.Error("abi 2 mask must contain REFER")
	}
	if v2&uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE) != 0 {
		t.Error("abi 2 mask must not contain TRUNCATE")
	}
	v3 := handledAccessForABI(3)
	if v3&uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE) == 0 {
		t.Error("abi 3 mask must contain TRUNCATE")
	}
	future := handledAccessForABI(9)
	if future != v3 {
		t.Errorf("future abi mask = %#x, want v3 mask %#x (request only known rights)", future, v3)
	}
}

func TestIsSpikeUnavailable(t *testing.T) {
	for _, err := range []error{unix.ENOSYS, unix.EPERM, unix.EOPNOTSUPP, unix.E2BIG, wrapErr(unix.ENOSYS)} {
		if !isSpikeUnavailable(err) {
			t.Errorf("isSpikeUnavailable(%v) = false, want true", err)
		}
	}
	for _, err := range []error{unix.EINVAL, fmt.Errorf("boom")} {
		if isSpikeUnavailable(err) {
			t.Errorf("isSpikeUnavailable(%v) = true, want false", err)
		}
	}
}

func wrapErr(err error) error { return fmt.Errorf("landlock spike: wrap: %w", err) }

// Compatibility corpus: representative shell and Git workflows run
// under confinement through shell mode (runSpikeShell). Findings below
// are the empirical basis for the production read-only system set.
//
// Recorded on Linux ABI 3 (WSL2 kernel 6.6; re-run on target runners
// via the sandbox-probe CI job before freezing defaults). All cases
// below pass with RW workspace (+tmp inside it), RO
// {usr,bin,lib,lib64,sbin,etc,dev}, and one narrow file rule:
//
//   - Basic shell (pwd/ls/cat/echo/redirection/mkdir/mv/rm), workspace
//     script execution, and in-workspace symlinks work.
//   - Git status/diff/log/add/commit/branch works with identity passed
//     explicitly (-c user.name/email, GIT_CONFIG_NOSYSTEM=1); $HOME is
//     deliberately ungranted and nothing in the tested flows needs it.
//     Git additionally needs /dev/null read+write (it opens it O_RDWR
//     early); the grant targets the file, not the whole /dev, so no
//     device-creation rights cross with it.
//   - /etc must be granted RO (user database, git system config, TLS
//     trust store); without it git and cert reads fail. An early
//     corpus revision omitted /etc and the outside-read case passed
//     for the wrong reason — it now targets an existing ungranted
//     file so the denial proves confinement.
//   - /proc reads are denied (no rule granted); nothing in the tested
//     flows needs them. Verdict: deny /proc by default.
//   - Reads outside workspace+system set fail closed with nonzero exit.
//
// Untested here (cannot be tested safely without privileges): nested
// mounts, overlay escapes, rule paths that are themselves mounts.
type corpusCase struct {
	name string
	// setup builds the parent-side fixture inside ws (unrestricted).
	// It may skip the case (e.g. git or certs absent).
	setup func(t *testing.T, ws string)
	// script is the bash -c body, run with cwd=ws under confinement.
	script string
	// want lists markers required in the script output.
	want []string
	// wantExit is the expected RUN-EXIT of the script. Nonzero
	// expectations prove lockdown (denial cases), never masking.
	wantExit int
}

func corpusCases() []corpusCase {
	seedFile := func(t *testing.T, ws string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, "seed.txt"), []byte("seed-content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seedRepo := func(t *testing.T, ws string) {
		t.Helper()
		if _, err := exec.LookPath("git"); err != nil {
			t.Skipf("git not on PATH: %v", err)
		}
		run := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = ws
			// Identity travels with the invocation, never via $HOME.
			cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture git %v: %v\n%s", args, err, out)
			}
		}
		run("init", "-q")
		if err := os.WriteFile(filepath.Join(ws, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "tracked.txt")
		run("-c", "user.name=corpus", "-c", "user.email=corpus@example.test", "commit", "-qm", "init")
	}
	nop := func(t *testing.T, ws string) { t.Helper() }
	return []corpusCase{
		{
			name:   "basic-pwd-ls",
			setup:  seedFile,
			script: `pwd && ls && echo BASIC-OK`,
			want:   []string{"seed.txt", "BASIC-OK"},
		},
		{
			name:   "cat-file",
			setup:  seedFile,
			script: `cat seed.txt`,
			want:   []string{"seed-content"},
		},
		{
			name:  "workspace-writes",
			setup: nop,
			script: `set -e
touch new.txt
mkdir -p sub/dir
echo hello > sub/dir/f.txt
mv sub/dir/f.txt sub/dir/g.txt
cat sub/dir/g.txt
rm sub/dir/g.txt
echo WRITES-OK`,
			want: []string{"hello", "WRITES-OK"},
		},
		{
			name: "symlink-inside",
			setup: func(t *testing.T, ws string) {
				t.Helper()
				seedFile(t, ws)
				if err := os.Symlink(filepath.Join(ws, "seed.txt"), filepath.Join(ws, "link")); err != nil {
					t.Fatal(err)
				}
			},
			script: `cat link`,
			want:   []string{"seed-content"},
		},
		{
			name: "script-exec",
			setup: func(t *testing.T, ws string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(ws, "hello.sh"), []byte("#!/bin/bash\necho SCRIPT-RAN\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			script: `./hello.sh`,
			want:   []string{"SCRIPT-RAN"},
		},
		{
			name:  "git-workflow",
			setup: seedRepo,
			script: `set -e
export GIT_CONFIG_NOSYSTEM=1
git status --short
git log --oneline | grep -q init
echo STAGED >> tracked.txt
git diff -- tracked.txt | grep -q STAGED
git add tracked.txt
git -c user.name=corpus -c user.email=corpus@example.test commit -qm second
git log --oneline | grep -q second
git checkout -qb feat
git checkout -q master 2>/dev/null || git checkout -q main || git checkout -q -
echo GIT-OK`,
			want: []string{"GIT-OK"},
		},
		{
			name: "certs-readable",
			setup: func(t *testing.T, ws string) {
				t.Helper()
				if _, err := os.Stat("/etc/ssl/certs/ca-certificates.crt"); err != nil {
					t.Skipf("system trust store absent: %v", err)
				}
			},
			script: `head -c 27 /etc/ssl/certs/ca-certificates.crt`,
			want:   []string{"-----BEGIN CERTIFICATE-----"},
		},
		{
			name:     "proc-denied",
			setup:    nop,
			script:   `cat /proc/version`,
			wantExit: 1,
		},
		{
			name:     "outside-read-denied",
			setup:    nop,
			script:   `cat "$FF_LANDLOCK_SPIKE_DENIED/secret.txt"`,
			wantExit: 1,
		},
	}
}

// TestLandlockCompatCorpus runs every workflow case in a fresh
// workspace under confinement. Each case builds one policy: RW
// workspace (+tmp inside it) and the candidate read-only system set.
// Cases assert exact RUN-EXIT codes plus output markers, so a denied
// operation where success was expected (or vice versa) fails loudly
// with the responsible command in the log.
func TestLandlockCompatCorpus(t *testing.T) {
	requireSpikeABI(t)
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not on PATH: %v", err)
	}
	for _, tc := range corpusCases() {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			tc.setup(t, ws)
			if err := os.MkdirAll(filepath.Join(ws, "tmp"), 0o755); err != nil {
				t.Fatal(err)
			}
			// Sibling denied tree: exists and is readable
			// unrestricted, granted no rule. Denials against it
			// prove confinement rather than missing files.
			denied := t.TempDir()
			if err := os.WriteFile(filepath.Join(denied, "secret.txt"), []byte("denied-marker"), 0o600); err != nil {
				t.Fatal(err)
			}
			policy := Policy{Version: PolicyVersion, NoNewPrivs: true, Allowed: []Rule{{Path: ws}}}
			addRule := func(path string, readOnly bool, access uint64) {
				for _, r := range policy.Allowed {
					if r.Path == path {
						return
					}
				}
				policy.Allowed = append(policy.Allowed, Rule{Path: path, ReadOnly: readOnly, Access: access})
			}
			for _, dir := range append([]string{filepath.Dir(bashPath)}, corpusRODirs...) {
				if st, err := os.Stat(dir); err != nil || !st.IsDir() {
					continue
				}
				addRule(dir, true, 0)
			}
			// /dev/null needs read+write (git opens it O_RDWR);
			// a whole-/dev RW grant would also permit device-node
			// creation rights, so the grant targets the file.
			if _, err := os.Stat("/dev/null"); err == nil {
				addRule("/dev/null", false, unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_WRITE_FILE)
			}
			code, combined := spawnSpikeChild(t, func(w *os.File) error {
				return WritePolicy(w, policy)
			}, []string{
				spikeShellEnv + "=1",
				spikeWorkdirEnv + "=" + ws,
				spikeScriptEnv + "=" + tc.script,
				"TMPDIR=" + filepath.Join(ws, "tmp"),
				spikeDeniedEnv + "=" + denied,
			})
			if code != 0 {
				if strings.Contains(combined, SentinelPrefix+"UNAVAILABLE") {
					t.Skipf("landlock blocked in this environment: %s", firstLine(combined))
				}
				t.Fatalf("shell-mode child exit %d, want 0 (result envelope):\n%s", code, combined)
			}
			exit, output := parseRunEnvelope(t, combined)
			if exit != tc.wantExit {
				t.Fatalf("RUN-EXIT=%d, want %d (script %q):\n%s", exit, tc.wantExit, tc.script, output)
			}
			for _, want := range tc.want {
				if !strings.Contains(output, want) {
					t.Errorf("output missing %q (script %q):\n%s", want, tc.script, output)
				}
			}
		})
	}
}

// parseRunEnvelope splits the child's "RUN-EXIT=<code>" header from the
// script output that follows it.
func parseRunEnvelope(t *testing.T, combined string) (int, string) {
	t.Helper()
	line, rest, ok := strings.Cut(combined, "\n")
	if !ok || !strings.HasPrefix(line, "RUN-EXIT=") {
		t.Fatalf("child output missing RUN-EXIT envelope:\n%s", combined)
	}
	var code int
	if _, err := fmt.Sscanf(strings.TrimPrefix(line, "RUN-EXIT="), "%d", &code); err != nil {
		t.Fatalf("malformed RUN-EXIT line %q", line)
	}
	return code, rest
}
