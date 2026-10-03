package suites

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forcefield/internal/hpov/bench"
)

// TestMain re-execs the test binary as a scripted fake ff when
// HPOV_SUITES_FAKE=1. Modes are driven by argv + env knobs:
//
//	--version        "ff version test" (or HPOV_FAKE_BAD=1 -> garbage)
//	--help           cobra-like help (or HPOV_FAKE_BAD=1 -> truncated)
//	run ...          exit HPOV_FAKE_EXIT (default 1); appends to
//	                 $HOME/hpov-fake-count.txt; with FF_PERF_MARKERS=1
//	                 emits stage markers unless HPOV_FAKE_NO_MARKERS=1;
//	                 prints the unknown-agent error unless
//	                 HPOV_FAKE_NO_ERROR=1.
func TestMain(m *testing.M) {
	if os.Getenv("HPOV_SUITES_FAKE") != "1" && os.Getenv("HPOV_TUI_FAKE") != "1" {
		os.Exit(m.Run())
	}
	os.Exit(dispatchFake(os.Args[1:]))
}

// dispatchFake routes bare invocations (the interactive TUI path) to
// the TUI fake and everything else to the launch fake.
func dispatchFake(args []string) int {
	if len(args) == 0 && os.Getenv("HPOV_TUI_FAKE") == "1" {
		return tuiFakeMain()
	}
	return fakeMain(args)
}

func fakeMain(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "--version":
		if os.Getenv("HPOV_FAKE_BAD") == "1" {
			_, _ = os.Stdout.WriteString("garbage\n")
			return 0
		}
		_, _ = os.Stdout.WriteString("ff version test\n")
		return 0
	case "--help":
		if os.Getenv("HPOV_FAKE_BAD") == "1" {
			_, _ = os.Stdout.WriteString("tiny\n")
			return 0
		}
		_, _ = os.Stdout.WriteString("Forcefield is a local-first agent harness.\n\nUsage:\n  ff [flags]\n")
		return 0
	case "run":
		if e := os.Getenv("HPOV_FAKE_EXIT"); e != "" {
			if e == "0" {
				return 0
			}
			if e == "2" {
				return 2
			}
		}
		if home, err := os.UserHomeDir(); err == nil {
			f, err := os.OpenFile(filepath.Join(home, "hpov-fake-count.txt"),
				os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				_, _ = f.WriteString("1")
				_ = f.Close()
			}
		}
		if os.Getenv("FF_PERF_MARKERS") != "" && os.Getenv("HPOV_FAKE_NO_MARKERS") != "1" {
			for _, ev := range []string{"stage-skills", "stage-memory", "stage-provider", "stage-tools", "stage-agents"} {
				_, _ = os.Stderr.WriteString("ff-perf " + ev + "\n")
			}
		}
		if os.Getenv("HPOV_FAKE_NO_ERROR") != "1" {
			_, _ = os.Stderr.WriteString("Error: unknown agent \"__bench_bogus__\": agent not found\n")
		}
		return 1
	}
	return 2
}

// fakeEnvVars is the environment the next fake subject attaches to its
// contract. It stands in for what a real profile would declare for its
// own harness: production reads the child environment from the
// contract, and these tests set it the same way, through the subject.
var fakeEnvVars = map[string]string{"HPOV_SUITES_FAKE": "1"}

// fakeSubject builds the re-exec fake under the Forcefield profile.
func fakeSubject(t *testing.T, extra ...string) bench.Subject {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for k, v := range fakeEnvVars {
		env[k] = v
	}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return ffSubject("fake", self, env)
}

// fakeEnv resets the knobs the next fake subject forwards.
func fakeEnv(t *testing.T, extra ...string) {
	t.Helper()
	// Reset per test: the scrubbed child environment only carries what
	// the contract forwards, so stale knobs must not leak across tests.
	fakeEnvVars = map[string]string{"HPOV_SUITES_FAKE": "1"}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		fakeEnvVars[k] = v
	}
}

func testRunEnv(t *testing.T) *bench.RunEnv {
	t.Helper()
	return &bench.RunEnv{Root: t.TempDir(), Profile: "quick", Seed: 1}
}

func TestLaunchRegistration(t *testing.T) {
	all := All()
	// 5 launch + 1 TUI timeline + 3 memory benchmarks.
	if want := len(LaunchIDs) + 1 + len(MemBenchmarkIDs); len(all) != want {
		t.Fatalf("registered = %d, want %d", len(all), want)
	}
	seen := map[string]int{}
	kinds := map[string]string{}
	for _, r := range all {
		s := r.Bench.Spec()
		seen[s.ID]++
		kinds[s.ID] = string(s.Kind)
		if s.Tier != 1 {
			t.Errorf("%s tier = %d, want 1", s.ID, s.Tier)
		}
		if s.DefinitionVersion != 1 {
			t.Errorf("%s definition_version = %d", s.ID, s.DefinitionVersion)
		}
		if s.Predicate == "" {
			t.Errorf("%s has no validity predicate", s.ID)
		}
		if s.Purpose == "" {
			t.Errorf("%s has no purpose contract", s.ID)
		}
	}
	for _, id := range LaunchIDs {
		if seen[id] != 1 {
			t.Errorf("%s registered %d times", id, seen[id])
		}
	}
	if kinds["launch.artifact-size"] != "static" {
		t.Errorf("artifact-size kind = %s", kinds["launch.artifact-size"])
	}
}

func TestVersionValidity(t *testing.T) {
	fakeEnv(t)
	b := &versionBench{}
	fx, err := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatal(err)
	}
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t), bench.Iter{Phase: bench.PhaseMeasure})
	if err != nil || !obs.Valid {
		t.Fatalf("valid version sample rejected: %v %+v", err, obs)
	}
	if !strings.HasPrefix(obs.Attrs["version"], "ff version") {
		t.Fatalf("version attr = %q", obs.Attrs["version"])
	}
}

func TestVersionRejectsGarbage(t *testing.T) {
	fakeEnv(t, "HPOV_FAKE_BAD=1")
	b := &versionBench{}
	fx, err := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatal(err)
	}
	// The bad mode only affects --version output.
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t), bench.Iter{Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("malformed output must be invalid, not an error: %v", err)
	}
	if obs.Valid {
		t.Fatal("garbage version output accepted as valid")
	}
	if obs.InvalidReason == "" {
		t.Fatal("invalid sample needs a reason")
	}
}

func TestHelpValidity(t *testing.T) {
	fakeEnv(t)
	b := &helpBench{}
	fx, err := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatal(err)
	}
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t), bench.Iter{Phase: bench.PhaseMeasure})
	if err != nil || !obs.Valid {
		t.Fatalf("valid help sample rejected: %v %+v", err, obs)
	}
	if obs.Attrs["stdout_bytes"] == "0" {
		t.Fatal("help byte count must be recorded")
	}
}

func TestHelpRejectsTruncated(t *testing.T) {
	fakeEnv(t, "HPOV_FAKE_BAD=1")
	b := &helpBench{}
	fx, _ := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t), bench.Iter{Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("truncated output must be invalid, not an error: %v", err)
	}
	if obs.Valid {
		t.Fatal("truncated help accepted as valid")
	}
}

func TestHeadlessSteadySharesHome(t *testing.T) {
	fakeEnv(t)
	b := &headlessBench{}
	subj := fakeSubject(t)
	env := testRunEnv(t)
	fx, err := b.Setup(context.Background(), env, subj)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		obs, err := b.Iterate(context.Background(), fx, subj, bench.Iter{Index: i, Phase: bench.PhaseMeasure})
		if err != nil || !obs.Valid {
			t.Fatalf("iter %d rejected: %v %+v", i, err, obs)
		}
		if obs.Attrs["exit_code"] != "1" {
			t.Fatalf("exit attr = %v", obs.Attrs)
		}
	}
	// Prime (1) + 2 iterations share one home: count file holds "111".
	raw, err := os.ReadFile(filepath.Join(fx.HomeDir, "hpov-fake-count.txt"))
	if err != nil || string(raw) != "111" {
		t.Fatalf("steady home not shared: %q err=%v", raw, err)
	}
	// A second subject gets an isolated parent.
	fx2, err := b.Setup(context.Background(), env, ffSubject("other", subj.Path, fakeEnvVars))
	if err != nil {
		t.Fatal(err)
	}
	if fx2.HomeDir == fx.HomeDir {
		t.Fatal("subjects must not share steady fixtures")
	}
}

func TestHeadlessFirstRunIsolates(t *testing.T) {
	fakeEnv(t)
	b := &headlessBench{firstRun: true}
	if got := b.Spec().PlanFor("standard").Warmup; got != 0 {
		t.Fatalf("first-run warmup = %d, want 0", got)
	}
	subj := fakeSubject(t)
	fx, err := b.Setup(context.Background(), testRunEnv(t), subj)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		fxIter := bench.Fixture{HomeDir: fx.HomeDir, WorkDir: fx.WorkDir, Timeout: time.Minute}
		obs, err := b.Iterate(context.Background(), fxIter, subj, bench.Iter{Index: i, Phase: bench.PhaseMeasure})
		if err != nil || !obs.Valid {
			t.Fatalf("iter %d rejected: %v %+v", i, err, obs)
		}
	}
	// Each iteration wrote exactly one mark in its own fresh home
	// (home-<index> under the parent).
	for i := 0; i < 3; i++ {
		h := filepath.Join(fx.HomeDir, "home-"+itoa(uint64(i)))
		raw, err := os.ReadFile(filepath.Join(h, "hpov-fake-count.txt"))
		if err != nil || string(raw) != "1" {
			t.Fatalf("home %s count = %q err=%v (want exactly one mark)", h, raw, err)
		}
	}
}

func TestHeadlessWrongExit(t *testing.T) {
	fakeEnv(t, "HPOV_FAKE_EXIT=0")
	b := &headlessBench{}
	subj := fakeSubject(t)
	_, err := b.Setup(context.Background(), testRunEnv(t), subj)
	if err == nil {
		t.Fatal("priming with exit 0 must fail setup (workload boundary unconfirmed)")
	}
	if se, ok := err.(*bench.SkipError); !ok || se.Status != "invalid" {
		t.Fatalf("want invalid SkipError, got %T %v", err, err)
	}
}

func TestHeadlessProbe(t *testing.T) {
	fakeEnv(t)
	b := &headlessBench{}
	subj := fakeSubject(t)
	fx, err := b.Setup(context.Background(), testRunEnv(t), subj)
	if err != nil {
		t.Fatal(err)
	}
	ok, checks, err := b.Probe(context.Background(), fx, subj)
	if err != nil || !ok {
		t.Fatalf("probe rejected: %v %v %+v", err, ok, checks)
	}
	for _, k := range []string{"exit_code_1", "unknown_agent_error", "stage_agents_seen"} {
		if !checks[k] {
			t.Errorf("check %s = false", k)
		}
	}
}

func TestHeadlessProbeNoMarkers(t *testing.T) {
	// Simulates a subject without perfmark: exit + error text hold,
	// but the stage boundary cannot be confirmed.
	fakeEnv(t, "HPOV_FAKE_NO_MARKERS=1")
	b := &headlessBench{}
	subj := fakeSubject(t)
	fx, err := b.Setup(context.Background(), testRunEnv(t), subj)
	if err != nil {
		t.Fatal(err)
	}
	ok, checks, err := b.Probe(context.Background(), fx, subj)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("probe must fail without stage markers")
	}
	if !checks["exit_code_1"] || !checks["unknown_agent_error"] {
		t.Fatalf("exit/error checks must still hold: %v", checks)
	}
	if checks["stage_agents_seen"] {
		t.Fatal("stage_agents_seen must be false without markers")
	}
}

func TestArtifactSize(t *testing.T) {
	b := &artifactSizeBench{}
	payload := []byte("fake-binary-payload-12345")
	p := filepath.Join(t.TempDir(), "ff-test.exe")
	if err := os.WriteFile(p, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	subj := bench.Subject{Label: "fake", Path: p}
	fx, err := b.Setup(context.Background(), testRunEnv(t), subj)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := b.Iterate(context.Background(), fx, subj, bench.Iter{Phase: bench.PhaseMeasure})
	if err != nil || !obs.Valid {
		t.Fatalf("artifact sample rejected: %v %+v", err, obs)
	}
	if obs.Values["size_bytes"] != float64(len(payload)) {
		t.Fatalf("size = %v, want %d", obs.Values["size_bytes"], len(payload))
	}
	if len(obs.Attrs["sha256"]) != 64 {
		t.Fatalf("sha256 attr = %q", obs.Attrs["sha256"])
	}
	// Missing binary: invalid + error, never a silent zero.
	obs, err = b.Iterate(context.Background(), fx, bench.Subject{Label: "ghost", Path: p + ".missing"}, bench.Iter{})
	if err == nil || obs.Valid {
		t.Fatal("missing binary must be invalid with an error")
	}
}

func TestMarkersPackageUsed(t *testing.T) {
	if !ffProto.Has("ff-perf stage-agents\n", "stage-agents") {
		t.Fatal("markers helper must detect stage-agents")
	}
}
