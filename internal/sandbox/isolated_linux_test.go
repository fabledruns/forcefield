//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forcefield/internal/sandbox/boundarytest"
	"forcefield/internal/sandbox/landlock"
)

// runIsolated executes one command through the isolated executor and
// returns its exit code and combined output. Cleanup runs on return.
func runIsolated(t *testing.T, ex Executor, command string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prepared, err := ex.Prepare(ctx, Request{Command: command})
	if err != nil {
		t.Fatalf("Prepare(%q): %v", command, err)
	}
	if prepared.Cleanup != nil {
		defer prepared.Cleanup()
	}
	out, err := prepared.Cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run %q: %v", command, err)
		}
	}
	return code, string(out)
}

// requireLandlockBackend returns an isolated executor over a throwaway
// workspace, or gates on the boundary harness when Landlock is
// unavailable: skips by default, fails when FF_REQUIRE_BOUNDARY
// requires it. Either way nothing runs unconfined.
func requireLandlockBackend(t *testing.T, ws string, creds []string) Executor {
	t.Helper()
	ex, err := NewExecutor(Policy{Mode: ModeIsolated, Workspace: ws, CredentialEnv: creds})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if err := ex.Probe(context.Background()); err != nil {
		boundarytest.RequireBoundary(t, "landlock", fmt.Sprintf("isolated backend unavailable: %v", err))
	}
	return ex
}

// TestLandlockBoundaryEnforced is the genuine boundary test: with a
// Landlock-capable kernel, shell text cannot read or write outside the
// workspace and private tmp, workspace operations succeed, provider
// credentials stay out of the child environment, and /proc stays
// unreadable. Reads through bash-spawned cat prove the restriction
// survives exec into descendants.
func TestLandlockBoundaryEnforced(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	const canaryName = "FF_ISOLATED_BOUNDARY_CANARY"
	t.Setenv(canaryName, "boundary-child-must-not-see")
	if err := os.WriteFile(ws+"/ok.txt", []byte("workspace-ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside+"/secret.txt", []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	ex := requireLandlockBackend(t, ws, []string{canaryName})

	if code, out := runIsolated(t, ex, "cat "+outside+"/secret.txt"); code == 0 {
		t.Errorf("outside read succeeded, want denial (out=%q)", out)
	}
	if code, out := runIsolated(t, ex, "echo planted > "+outside+"/evil.txt"); code == 0 {
		t.Errorf("outside write succeeded, want denial (out=%q)", out)
	} else if _, err := os.Stat(outside + "/evil.txt"); !os.IsNotExist(err) {
		t.Errorf("outside write landed despite nonzero exit: %v", err)
	}
	if code, out := runIsolated(t, ex, "cat /proc/version"); code == 0 {
		t.Errorf("/proc read succeeded, want denial (out=%q)", out)
	}
	if code, out := runIsolated(t, ex, "cat "+ws+"/ok.txt"); code != 0 || !strings.Contains(out, "workspace-ok") {
		t.Errorf("workspace read exit=%d, want 0 with content (out=%q)", code, out)
	}
	if code, out := runIsolated(t, ex, "echo hello > new.txt && mkdir -p sub && mv new.txt sub/renamed.txt && cat sub/renamed.txt"); code != 0 || !strings.Contains(out, "hello") {
		t.Errorf("workspace write/move/read exit=%d, want 0 with content (out=%q)", code, out)
	}
	if code, out := runIsolated(t, ex, `[ -n "$FF_ISOLATED_BOUNDARY_CANARY" ] && echo LEAKED || echo CLEAN`); code != 0 || !strings.Contains(out, "CLEAN") || strings.Contains(out, "LEAKED") {
		t.Errorf("credential leaked or unexpected failure: exit=%d out=%q", code, out)
	}
}

// TestIsolatedFailsClosedWithoutLandlock pins the fail-closed rule
// with a stubbed unavailable kernel: Probe and Prepare refuse with
// ErrBackendUnavailable instead of running unconfined. Runs on any
// Linux machine, capable or not.
func TestIsolatedFailsClosedWithoutLandlock(t *testing.T) {
	orig := landlockABIFunc
	t.Cleanup(func() { landlockABIFunc = orig })
	landlockABIFunc = func() (int, error) { return -1, fmt.Errorf("landlock spike: ABI query: function not implemented") }

	ex, err := NewExecutor(Policy{Mode: ModeIsolated, Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if err := ex.Probe(context.Background()); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("Probe error = %v, want ErrBackendUnavailable", err)
	}
	if _, err := ex.Prepare(context.Background(), Request{Command: "echo hi"}); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("Prepare error = %v, want ErrBackendUnavailable", err)
	}
}

// TestIsolatedProbeValidatesExtras pins early failure: a missing
// extra path fails Probe (startup/doctor time) with a backend error,
// not per-command inside the helper. Kernel-independent via seams.
func TestIsolatedProbeValidatesExtras(t *testing.T) {
	origABI := landlockABIFunc
	origBash := bashLookPath
	t.Cleanup(func() { landlockABIFunc = origABI; bashLookPath = origBash })
	landlockABIFunc = func() (int, error) { return 3, nil }
	bashLookPath = func(string) (string, error) { return "/bin/bash", nil }

	ex, err := NewExecutor(Policy{
		Mode:      ModeIsolated,
		Workspace: t.TempDir(),
		FSWrite:   []string{filepath.Join(t.TempDir(), "missing")},
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if err := ex.Probe(context.Background()); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("Probe error = %v, want ErrBackendUnavailable for the missing extra", err)
	}
}

// TestIsolatedPrepareRefusesMissingBash pins the interpreter grant as
// load-bearing: an unresolvable bash refuses at Prepare instead of
// reaching the helper with no executable rule. Kernel-independent via
// seams.
func TestIsolatedPrepareRefusesMissingBash(t *testing.T) {
	origABI := landlockABIFunc
	origBash := bashLookPath
	t.Cleanup(func() { landlockABIFunc = origABI; bashLookPath = origBash })
	landlockABIFunc = func() (int, error) { return 3, nil }
	// A resolved path under a missing directory: the interpreter-dir
	// grant cannot resolve, so Prepare must refuse instead of reaching
	// the helper with no executable rule.
	missingBash := filepath.Join(t.TempDir(), "missing-dir", "bash")
	bashLookPath = func(string) (string, error) { return missingBash, nil }

	ex, err := NewExecutor(Policy{Mode: ModeIsolated, Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := ex.Prepare(context.Background(), Request{Command: "echo hi"}); err == nil {
		t.Fatal("Prepare succeeded with an unresolvable interpreter, want refusal")
	} else if !strings.Contains(err.Error(), "interpreter") {
		t.Errorf("Prepare error = %v, want it to name the interpreter directory", err)
	}
}

// TestIsolatedDescribeHonesty pins truthful reporting in both states:
// confinement claimed only when support probed OK, with the stable
// filesystem.landlock ID either way.
func TestIsolatedDescribeHonesty(t *testing.T) {
	ex, err := NewExecutor(Policy{Mode: ModeIsolated, Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	d := ex.Describe(context.Background())
	if d.Mode != ModeIsolated {
		t.Errorf("Mode = %q, want isolated", d.Mode)
	}
	ids := limitationIDs(d)
	l, ok := ids[LimFilesystemLandlock]
	if !ok {
		t.Fatalf("missing %q limitation", LimFilesystemLandlock)
	}
	if _, err := ex.(interface{ support() (int, error) }).support(); err != nil {
		if d.FilesystemConfined {
			t.Error("Describe claims confinement while support fails")
		}
		if !l.Warn {
			t.Errorf("%q must warn when unavailable", LimFilesystemLandlock)
		}
	} else {
		if !d.FilesystemConfined {
			t.Error("Describe denies confinement while support probes OK")
		}
		if l.Warn {
			t.Errorf("%q must be info when enforced", LimFilesystemLandlock)
		}
		if lines := strings.Join(d.SummaryLines(), "\n"); !strings.Contains(lines, "Landlock ABI") {
			t.Errorf("SummaryLines must report the enforced ABI:\n%s", lines)
		}
	}
	if _, ok := ids[LimShellTextOpen]; ok {
		t.Errorf("%q (never confined, any mode) must not appear for isolated", LimShellTextOpen)
	}
	if _, ok := ids[LimShellTextConfined]; !ok {
		t.Errorf("missing %q limitation", LimShellTextConfined)
	}
	if got := d.isolation(); !strings.Contains(got, "Landlock") {
		t.Errorf("isolation() = %q, want Landlock wording", got)
	}
}

// TestIsolatedHelperRejectsCorruptTransport proves the helper fails
// closed on a corrupt policy pipe: the re-executed test binary exits
// 126 with the FFSBX sentinel instead of running anything.
func TestIsolatedHelperRejectsCorruptTransport(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cmd := exec.CommandContext(ctx, exe, HelperArg)
	cmd.ExtraFiles = []*os.File{r}
	var out strings.Builder
	cmd.Stdout = &out
	var errOut strings.Builder
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		t.Fatalf("child start: %v", err)
	}
	_, _ = w.Write([]byte("CORRUPT"))
	_ = w.Close()
	waitErr := cmd.Wait()
	code := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("child wait: %v", waitErr)
		}
	}
	combined := out.String() + errOut.String()
	if code != landlock.ExitSetupFailed {
		t.Errorf("exit %d, want %d (setup failure):\n%s", code, landlock.ExitSetupFailed, combined)
	}
	if !strings.Contains(combined, landlock.SentinelPrefix) {
		t.Errorf("missing %q sentinel:\n%s", landlock.SentinelPrefix, combined)
	}
}

// TestIsolatedRulesResolveCanonical pins rule construction without a
// kernel: workspace and extras resolve to canonical paths, missing
// extras fail closed, and the system set only adds existing dirs.
func TestIsolatedRulesResolveCanonical(t *testing.T) {
	ws := t.TempDir()
	extra := t.TempDir()
	rules, err := isolatedRules(ws, t.TempDir(), Policy{FSWrite: []string{extra}})
	if err != nil {
		t.Fatalf("isolatedRules: %v", err)
	}
	paths := make(map[string]bool)
	for _, r := range rules {
		paths[r.Path] = true
	}
	if !paths[ws] || !paths[extra] {
		t.Errorf("rules missing workspace or extra: %+v", rules)
	}
	if _, err := isolatedRules(ws, t.TempDir(), Policy{FSWrite: []string{filepath.Join(ws, "does-not-exist")}}); err == nil {
		t.Error("missing extra write path accepted, want fail-closed refusal")
	}
	if _, err := isolatedRules(ws, t.TempDir(), Policy{FSRead: []string{""}}); err == nil {
		t.Error("empty extra read path accepted, want fail-closed refusal")
	}
}

// TestIsolatedRulesExplicitOverrides pins precedence: an extra naming
// a built-in path replaces the built-in grant instead of being
// silently ignored.
func TestIsolatedRulesExplicitOverrides(t *testing.T) {
	ws := t.TempDir()
	tmp := t.TempDir()
	rules, err := isolatedRules(ws, tmp, Policy{FSRead: []string{tmp}})
	if err != nil {
		t.Fatalf("isolatedRules: %v", err)
	}
	count := 0
	for _, r := range rules {
		if r.Path == tmp {
			count++
			if !r.ReadOnly {
				t.Errorf("tmp rule = %+v, want read-only override", r)
			}
		}
	}
	if count != 1 {
		t.Errorf("tmp appears %d times in rules, want exactly once: %+v", count, rules)
	}
}
