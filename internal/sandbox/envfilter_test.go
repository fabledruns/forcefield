package sandbox

import (
	"context"
	"strings"
	"testing"
)

func TestStripCredentialEnv(t *testing.T) {
	cases := []struct {
		name  string
		env   []string
		strip []string
		want  []string
	}{
		{"empty strip list keeps everything", []string{"A=1", "B=2"}, nil, []string{"A=1", "B=2"}},
		{"exact match removed", []string{"NVIDIA_API_KEY=s3cr3t", "PATH=/bin"}, []string{"NVIDIA_API_KEY"}, []string{"PATH=/bin"}},
		{"prefix does not match", []string{"NVIDIA_API_KEY_OLD=x"}, []string{"NVIDIA_API_KEY"}, []string{"NVIDIA_API_KEY_OLD=x"}},
		{"value containing name untouched", []string{"NOTE=NVIDIA_API_KEY"}, []string{"NVIDIA_API_KEY"}, []string{"NOTE=NVIDIA_API_KEY"}},
		{"empty names ignored", []string{"A=1"}, []string{""}, []string{"A=1"}},
		{"bare entry without equals", []string{"STRAY", "A=1"}, []string{"STRAY"}, []string{"A=1"}},
		{"empty value still stripped", []string{"K=", "A=1"}, []string{"K"}, []string{"A=1"}},
	}
	for _, tc := range cases {
		if got := stripCredentialEnv(tc.env, tc.strip); !equalEnvs(got, tc.want) {
			t.Errorf("%s: stripCredentialEnv(%v, %v) = %v, want %v", tc.name, tc.env, tc.strip, got, tc.want)
		}
	}
}

func TestStripCredentialEnvDoesNotMutateInput(t *testing.T) {
	env := []string{"K=v", "A=1"}
	stripCredentialEnv(env, []string{"K"})
	if !equalEnvs(env, []string{"K=v", "A=1"}) {
		t.Errorf("input mutated: %v", env)
	}
}

// TestStripCredentialEnvCaseFoldsOnWindows pins the platform semantic:
// Windows environment names are case-insensitive, so a differently
// cased spelling must still strip there (and only there).
func TestStripCredentialEnvCaseFoldsOnWindows(t *testing.T) {
	orig := runtimeCaseInsensitive
	t.Cleanup(func() { runtimeCaseInsensitive = orig })

	runtimeCaseInsensitive = func() bool { return false }
	if got := stripCredentialEnv([]string{"nvidia_api_key=x"}, []string{"NVIDIA_API_KEY"}); !equalEnvs(got, []string{"nvidia_api_key=x"}) {
		t.Errorf("unix: case must matter, got %v", got)
	}

	runtimeCaseInsensitive = func() bool { return true }
	if got := stripCredentialEnv([]string{"nvidia_api_key=x", "PATH=y"}, []string{"NVIDIA_API_KEY"}); !equalEnvs(got, []string{"PATH=y"}) {
		t.Errorf("windows: case must fold, got %v", got)
	}
}

func equalEnvs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestNativePrepareStripsCredentials asserts on the constructed command
// (no spawn): a policy-listed credential name set in the test process
// must not reach cmd.Env, while an unlisted variable must.
func TestNativePrepareStripsCredentials(t *testing.T) {
	const canaryName = "FF_SANDBOX_STRIP_CANARY"
	const canaryValue = "strip-me-must-not-reach-child"
	const benignName = "FF_SANDBOX_STRIP_BENIGN"
	t.Setenv(canaryName, canaryValue)
	t.Setenv(benignName, "keep-me")

	ex, err := NewExecutor(Policy{Mode: ModeNative, CredentialEnv: []string{canaryName}})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	prepared, err := ex.Prepare(context.Background(), Request{Command: "true", Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if prepared.Cleanup != nil {
		defer prepared.Cleanup()
	}
	for _, kv := range prepared.Cmd.Env {
		if strings.Contains(kv, canaryValue) {
			t.Fatal("policy-listed credential value reached the child environment")
		}
		if name, _, _ := strings.Cut(kv, "="); name == canaryName {
			t.Fatalf("policy-listed credential name %q reached the child environment", canaryName)
		}
	}
	if v, ok := lookupEnv(prepared.Cmd.Env, benignName); !ok || v != "keep-me" {
		t.Errorf("unlisted variable %q missing or altered in child environment", benignName)
	}
}

// TestNativePrepareExplicitExtraWins pins the precedence: stripping
// applies to the inherited host environment only. A value deliberately
// set for the command via ExtraEnv still reaches the child.
func TestNativePrepareExplicitExtraWins(t *testing.T) {
	const name = "FF_SANDBOX_STRIP_OVERRIDE"
	t.Setenv(name, "host-value-must-lose")

	ex, err := NewExecutor(Policy{Mode: ModeNative, CredentialEnv: []string{name}})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	prepared, err := ex.Prepare(context.Background(), Request{
		Command:  "true",
		Dir:      t.TempDir(),
		ExtraEnv: []string{name + "=explicit-value"},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if prepared.Cleanup != nil {
		defer prepared.Cleanup()
	}
	v, ok := lookupEnv(prepared.Cmd.Env, name)
	if runtimeCaseInsensitive() {
		// Windows native relays extras inside the distribution argv
		// (/usr/bin/env), not in the launcher environment: the
		// explicit pair must travel there instead.
		if !strings.Contains(strings.Join(prepared.Cmd.Args, " "), name+"=explicit-value") {
			t.Errorf("explicit ExtraEnv missing from relay argv: %q", prepared.Cmd.Args)
		}
		if ok {
			t.Errorf("launcher environment carries %q=%q; host credential names must stay out of it", name, v)
		}
		return
	}
	if !ok || v != "explicit-value" {
		t.Errorf("explicit ExtraEnv lost: got %q,%v, want explicit-value,true", v, ok)
	}
}

// TestNativeDescribeReportsStripping pins honest reporting: the new
// limitation appears only when stripping is configured, and the default
// policy describes exactly what it did before.
func TestNativeDescribeReportsStripping(t *testing.T) {
	plain, err := NewExecutor(DefaultPolicy())
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if ids := limitationIDs(plain.Describe(context.Background())); ids[LimEnvCredsStripped].ID != "" {
		t.Error("default policy must not report credential stripping")
	}

	stripped, err := NewExecutor(Policy{Mode: ModeNative, CredentialEnv: []string{"NVIDIA_API_KEY"}})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	d := stripped.Describe(context.Background())
	l, ok := limitationIDs(d)[LimEnvCredsStripped]
	if !ok {
		t.Fatalf("configured policy missing %q limitation", LimEnvCredsStripped)
	}
	if l.Warn {
		t.Errorf("%q must be info, not a warning", LimEnvCredsStripped)
	}
	if !d.EnvForwarded {
		t.Error("EnvForwarded must stay true: host env still flows minus credential names")
	}
	lines := strings.Join(d.SummaryLines(), "\n")
	if !strings.Contains(lines, "minus Forcefield credential variables") {
		t.Errorf("SummaryLines must qualify the environment line when stripping:\n%s", lines)
	}
	if !strings.Contains(lines, "none") {
		t.Errorf("SummaryLines must still report isolation none:\n%s", lines)
	}
}

func lookupEnv(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}
