package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

// canaryExecutor stubs the WSL executor surface doctorInteropCanary
// needs, without a distribution: enforcement facts plus a scripted
// canary outcome.
type canaryExecutor struct {
	sandbox.Enforcement
	found []string
	err   error
}

func (c *canaryExecutor) Prepare(context.Context, sandbox.Request) (*sandbox.Prepared, error) {
	return nil, nil
}
func (c *canaryExecutor) Probe(context.Context) error { return nil }
func (c *canaryExecutor) Describe(context.Context) sandbox.Enforcement {
	return c.Enforcement
}
func (c *canaryExecutor) InteropCanary(context.Context) ([]string, error) {
	return c.found, c.err
}

func collectCanary(t *testing.T, ex sandbox.Executor, enc sandbox.Enforcement) ([]string, []verdict) {
	t.Helper()
	var lines []string
	var verdicts []verdict
	report := func(v verdict, format string, args ...any) {
		lines = append(lines, doctorLine(v, format, args...))
		verdicts = append(verdicts, v)
	}
	doctorInteropCanary(ex, enc, report)
	return lines, verdicts
}

func enforcedWSLEnc() sandbox.Enforcement {
	return sandbox.Enforcement{Mode: sandbox.ModeWSL, Network: sandbox.NetworkDisabled, NetworkEnforced: true}
}

// Observed helpers warn concretely; the verdict never depends on
// English substrings.
func TestDoctorInteropCanaryFoundWarns(t *testing.T) {
	ex := &canaryExecutor{Enforcement: enforcedWSLEnc(), found: []string{"cmd.exe", "curl.exe"}}
	lines, verdicts := collectCanary(t, ex, ex.Enforcement)
	if len(lines) != 1 || verdicts[0] != vWarn {
		t.Fatalf("found canary must warn once, got %v %v", verdicts, lines)
	}
	if !strings.Contains(lines[0], "cmd.exe") || !strings.Contains(lines[0], "host networking") {
		t.Errorf("canary warn must name helpers and host networking: %q", lines[0])
	}
}

// Empty resolution stays informational and keeps the structural gap
// reference: absence never upgrades the claim.
func TestDoctorInteropCanaryEmptyStaysHonest(t *testing.T) {
	ex := &canaryExecutor{Enforcement: enforcedWSLEnc()}
	lines, verdicts := collectCanary(t, ex, ex.Enforcement)
	if len(lines) != 1 || verdicts[0] != vOK {
		t.Fatalf("empty canary must report ok once, got %v %v", verdicts, lines)
	}
	if !strings.Contains(lines[0], sandbox.LimNetworkInterop) {
		t.Errorf("empty canary must reference the structural gap: %q", lines[0])
	}
}

// Errors stay a warning, biased to treating interop as present.
func TestDoctorInteropCanaryErrorWarns(t *testing.T) {
	ex := &canaryExecutor{Enforcement: enforcedWSLEnc(), err: errors.New("boom")}
	lines, verdicts := collectCanary(t, ex, ex.Enforcement)
	if len(lines) != 1 || verdicts[0] != vWarn {
		t.Fatalf("errored canary must warn once, got %v %v", verdicts, lines)
	}
}

// Non-enforced modes make no isolation claim to check: silence.
func TestDoctorInteropCanarySkippedWhenUnenforced(t *testing.T) {
	for _, enc := range []sandbox.Enforcement{
		{Mode: sandbox.ModeNative},
		{Mode: sandbox.ModeWSL, Network: sandbox.NetworkHost},
		{Mode: sandbox.ModeWSL, Network: sandbox.NetworkDisabled},
	} {
		lines, _ := collectCanary(t, &canaryExecutor{Enforcement: enc}, enc)
		if len(lines) != 0 {
			t.Errorf("mode %+v must skip the canary, got %v", enc, lines)
		}
	}
}
