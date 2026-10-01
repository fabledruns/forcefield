//go:build windows

package sandbox

import (
	"context"
	"strings"
	"testing"
)

// Phase 1 (Windows only): the WSL executor must carry the Phase 0
// interop caveat in both structured Limitations and approval-UI Notes,
// with the configured distro resolved into Enforcement.Distro.

func TestWSLDescribeHasInteropLimitation(t *testing.T) {
	dir := t.TempDir()
	e := healthyRestricted(t, dir, true)
	d := e.Describe(context.Background())
	if !d.NetworkEnforced {
		t.Fatal("expected enforced network with healthy probe")
	}
	found := false
	for _, l := range d.Limitations {
		if l.ID == LimNetworkInterop {
			found = true
			if !l.Warn {
				t.Error("network.wsl-interop must warn")
			}
			if !strings.Contains(l.Detail, ".exe") {
				t.Errorf("interop detail must name .exe execution: %q", l.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("missing %q in %+v", LimNetworkInterop, d.Limitations)
	}
	lines := strings.Join(d.SummaryLines(), "\n")
	if !strings.Contains(lines, ".exe") || !strings.Contains(lines, "NOT blocked") {
		t.Errorf("SummaryLines must carry the interop caveat for the approval UI:\n%s", lines)
	}
	for _, id := range []string{LimShellStagedVisible, LimProcessWSLRelay, LimFilesystemToolsCaged} {
		ok := false
		for _, l := range d.Limitations {
			if l.ID == id {
				ok = true
			}
		}
		if !ok {
			t.Errorf("missing limitation %q", id)
		}
	}
}

func TestWSLDescribeResolvesConfiguredDistro(t *testing.T) {
	dir := t.TempDir()
	e, err := newWSLExecutor(Policy{Mode: ModeWSL, Workspace: dir, Distro: "Ubuntu-22.04"})
	if err != nil {
		t.Fatal(err)
	}
	e.healthy = true
	e.netProbe = &netProbeResult{supported: true, unshare: "/usr/bin/unshare"}
	d := e.Describe(context.Background())
	if d.Distro != "Ubuntu-22.04" {
		t.Errorf("Distro = %q, want configured value", d.Distro)
	}
	lines := strings.Join(d.SummaryLines(), "\n")
	if !strings.Contains(lines, "Ubuntu-22.04") {
		t.Errorf("SummaryLines must name the configured distro:\n%s", lines)
	}
}

// Phase 5: the enforced-network line must qualify the claim to Linux
// sockets, so neither the approval UI nor doctor can read as complete
// egress denial while .exe interop keeps host networking.
func TestWSLDescribeNetworkLineQualified(t *testing.T) {
	dir := t.TempDir()
	e := healthyRestricted(t, dir, true)
	lines := strings.Join(e.Describe(context.Background()).SummaryLines(), "\n")
	if !strings.Contains(lines, "disabled - enforced for Linux sockets") {
		t.Errorf("network line must qualify Linux sockets:\n%s", lines)
	}
	if strings.Contains(lines, "disabled - enforced (isolated network namespace)") {
		t.Errorf("unqualified egress claim must not appear:\n%s", lines)
	}
}

// Phase 5: the interop canary against a real distribution resolves only
// known helpers (contract pin) and never errors the test when interop
// is absent — absence keeps the structural warning, it never upgrades
// the claim. Skips where WSL is unavailable (CI without a distro).
func TestWSLInteropCanaryResolvesKnownHelpers(t *testing.T) {
	dir := t.TempDir()
	e, err := newWSLExecutor(Policy{Mode: ModeWSL, Workspace: dir})
	if err != nil {
		t.Fatalf("newWSLExecutor: %v", err)
	}
	if err := e.Probe(context.Background()); err != nil {
		t.Skipf("WSL backend unavailable on this machine: %v", err)
	}
	found, err := e.InteropCanary(context.Background())
	if err != nil {
		t.Fatalf("InteropCanary: %v", err)
	}
	known := map[string]bool{}
	for _, h := range interopCanaryHelpers {
		known[h] = true
	}
	for _, f := range found {
		if !known[f] {
			t.Errorf("canary resolved unexpected helper %q", f)
		}
	}
	t.Logf("interop canary resolved inside the namespace: %v", found)
}
