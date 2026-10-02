// Launch benchmarks: process-level end-to-end measurement of the ff
// binary using only externally observable boundaries (spawn, exit,
// exit code, stdout/stderr text, markers on a separate pass).
//
// Contracts:
//
//	launch.version — `ff --version` in an isolated workdir.
//	  Start: immediately before process start. End: Wait returns.
//	  Exit 0, stdout begins with "ff version". Steady profile (home
//	  pre-created, though --version never touches config).
//	  Metrics: wall_ms (primary), cpu_ms = user+sys (secondary).
//
//	launch.help — `ff --help`, same boundaries. Exit 0, stdout
//	  contains "Usage:". Metrics: wall_ms, cpu_ms. Help byte count
//	  recorded per iteration (command-set drift changes help size).
//
//	launch.headless-init.steady — `ff run --agent __bench_bogus__ x`
//	  with an existing isolated home + workdir: config load, skills,
//	  repo root, memory store, provider, tools, policy/sandbox,
//	  agents, then deliberate local exit at agent validation.
//	  Exit 1 BY DESIGN (unknown agent after the full runtime is
//	  built). Validity per sample: exit_code==1. Per run: pre/post
//	  marker probes must see the unknown-agent error AND
//	  `stage-agents`. Metrics: wall_ms, cpu_ms.
//	  Steady = home primed once (untimed) so config.yaml exists;
//	  every measured iteration shares the same preconditions and
//	  performs no setup work inside the timed region.
//
//	launch.headless-init.first-run — same workload, fresh isolated
//	  home + workdir per iteration (config.Dir creates+chmods
//	  ~/.forcefield, config.Load writes config.yaml and prints
//	  "Created default config" to stderr). No warm-up. This is NOT
//	  an OS-cold measurement: "first-run" means fresh home, never a
//	  cold file cache.
//
//	launch.artifact-size — stat() + sha256 of the subject binary.
//	  Exact byte size, deterministic; executable size is not memory
//	  usage. Repeated 3x for equality.
package suites

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/fixture"
	"forcefield/internal/hpov/markers"
	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/spawn"
)

// sentinelAgent exercises the full runtime construction path and
// then fails local agent validation. Kept identical to
// bench/startup.ps1 for continuity.
const sentinelAgent = "__bench_bogus__"

// LaunchIDs lists the launch family in registration order.
var LaunchIDs = []string{
	"launch.version",
	"launch.help",
	"launch.headless-init.steady",
	"launch.headless-init.first-run",
	"launch.artifact-size",
}

// LaunchBenchmarks returns the launch family constructors.
func LaunchBenchmarks() []bench.Benchmark {
	return []bench.Benchmark{
		&versionBench{},
		&helpBench{},
		&headlessBench{firstRun: false},
		&headlessBench{firstRun: true},
		&artifactSizeBench{},
	}
}

// fixtureSeq disambiguates repeated Setup calls with identical
// (benchmark, subject) names; the subject label is the primary key.
var fixtureSeq atomic.Uint64

// launchParent creates a per-(benchmark, subject) parent directory
// under the run root so subjects never share fixture state.
func launchParent(root, id string, subj bench.Subject) (string, error) {
	name := strings.ReplaceAll(id, ".", "-") + "-" + sanitizeLabel(subj.Label) +
		"-" + itoa(fixtureSeq.Add(1))
	parent := filepath.Join(root, name)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create fixture parent: %w", err)
	}
	return parent, nil
}

// sanitizeLabel keeps fixture paths predictable for user labels.
func sanitizeLabel(s string) string {
	if s == "" {
		return "shared"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return "shared"
	}
	return b.String()
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// freshDirs creates one isolated home + workdir pair. Creation always
// happens outside the timed region (spawn.Run starts its clock at
// process start).
func freshDirs(parent, tag string) (home, work string, err error) {
	if home, err = fixture.NewHome(parent, "home-"+tag); err != nil {
		return "", "", err
	}
	if work, err = fixture.NewWorkDir(parent, "work-"+tag); err != nil {
		return "", "", err
	}
	return home, work, nil
}

// launchEnv builds the scrubbed child environment with home
// isolation (USERPROFILE on Windows, HOME on Unix). markers enables
// FF_PERF_MARKERS for the marker pass only; the wall pass never sets
// it, so marker perturbation (ReadMemStats STW, stderr writes) is
// never inside headline samples.
func launchEnv(home string, withMarkers bool) []string {
	set := fixture.HomeEnv(home)
	if withMarkers {
		set["FF_PERF_MARKERS"] = "1"
	}
	for k, v := range testEnvPassthrough {
		set[k] = v
	}
	env, _ := fixture.ScrubEnv(set)
	return env
}

// testEnvPassthrough forwards extra variables into the scrubbed child
// environment. Production leaves it empty (like the repo's other
// package-var test seams: rootTuiStarter, runtimeRun). The launch
// tests use it to steer the re-exec fake subject.
var testEnvPassthrough = map[string]string{}

func headlessArgs() []string {
	return []string{"run", "--agent", sentinelAgent, "x"}
}

// cpuAttr splits kernel CPU time for the secondary metric.
func cpuMs(res spawn.Result) float64 {
	return res.UserMS + res.SysMS
}

type versionBench struct{}

func (b *versionBench) Spec() bench.Spec {
	return bench.Spec{
		ID:                "launch.version",
		DefinitionVersion: 1,
		Title:             "CLI floor: --version",
		Purpose: "OS process creation + Go runtime/package init + cobra parse of " +
			"--version. Not headline startup speed: it calibrates how much of " +
			"every other launch number is fixed cost. Exit 0, stdout begins " +
			"with \"ff version\".",
		Kind:      bench.KindE2E,
		Tier:      1,
		Metrics:   launchMetrics(),
		Params:    map[string]string{"workload": "ff --version", "profile": "steady"},
		Predicate: "exit_code==0 and stdout starts with 'ff version'",
	}
}

func launchMetrics() []bench.MetricSpec {
	return []bench.MetricSpec{
		{Name: "wall_ms", Unit: "ms", Direction: bench.LowerIsBetter},
		{Name: "cpu_ms", Unit: "ms", Direction: bench.LowerIsBetter},
	}
}

func (b *versionBench) Setup(_ context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	parent, err := launchParent(env.Root, "launch.version", subj)
	if err != nil {
		return bench.Fixture{}, err
	}
	home, work, err := freshDirs(parent, "shared")
	if err != nil {
		return bench.Fixture{}, err
	}
	return bench.Fixture{HomeDir: home, WorkDir: work, Timeout: planTimeout(b.Spec(), env.Profile)}, nil
}

func (b *versionBench) Iterate(ctx context.Context, fx bench.Fixture, subj bench.Subject, _ bench.Iter) (bench.Observation, error) {
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subj.Path, Args: []string{"--version"},
		Env: launchEnv(fx.HomeDir, false), Dir: fx.WorkDir,
		CaptureStdout: true, Timeout: fx.Timeout,
	})
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	first := strings.TrimSpace(strings.SplitN(res.Stdout, "\n", 2)[0])
	obs := bench.Observation{
		Values: map[string]float64{"wall_ms": res.WallMS, "cpu_ms": cpuMs(res)},
		Valid:  res.ExitCode == 0 && !res.TimedOut && strings.HasPrefix(first, "ff version"),
		Attrs: map[string]string{
			"exit_code":    itoa(uint64(res.ExitCode)),
			"stdout_bytes": itoa(uint64(len(res.Stdout))),
			"version":      truncate(first, 80),
		},
	}
	if !obs.Valid {
		obs.InvalidReason = "expected exit 0 with 'ff version' stdout"
	}
	return obs, nil
}

func (b *versionBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

type helpBench struct{}

func (b *helpBench) Spec() bench.Spec {
	return bench.Spec{
		ID:                "launch.help",
		DefinitionVersion: 1,
		Title:             "CLI floor: --help",
		Purpose: "Same floor as --version plus cobra help rendering. Exit 0, " +
			"stdout contains \"Usage:\". Help byte count is recorded per " +
			"iteration: command-set drift changes help size.",
		Kind:      bench.KindE2E,
		Tier:      1,
		Metrics:   launchMetrics(),
		Params:    map[string]string{"workload": "ff --help", "profile": "steady"},
		Predicate: "exit_code==0 and stdout contains 'Usage:'",
	}
}

func (b *helpBench) Setup(_ context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	parent, err := launchParent(env.Root, "launch.help", subj)
	if err != nil {
		return bench.Fixture{}, err
	}
	home, work, err := freshDirs(parent, "shared")
	if err != nil {
		return bench.Fixture{}, err
	}
	return bench.Fixture{HomeDir: home, WorkDir: work, Timeout: planTimeout(b.Spec(), env.Profile)}, nil
}

func (b *helpBench) Iterate(ctx context.Context, fx bench.Fixture, subj bench.Subject, _ bench.Iter) (bench.Observation, error) {
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subj.Path, Args: []string{"--help"},
		Env: launchEnv(fx.HomeDir, false), Dir: fx.WorkDir,
		CaptureStdout: true, Timeout: fx.Timeout,
	})
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	obs := bench.Observation{
		Values: map[string]float64{"wall_ms": res.WallMS, "cpu_ms": cpuMs(res)},
		Valid:  res.ExitCode == 0 && !res.TimedOut && strings.Contains(res.Stdout, "Usage:"),
		Attrs: map[string]string{
			"exit_code":    itoa(uint64(res.ExitCode)),
			"stdout_bytes": itoa(uint64(len(res.Stdout))),
		},
	}
	if !obs.Valid {
		obs.InvalidReason = "expected exit 0 with 'Usage:' stdout"
	}
	return obs, nil
}

func (b *helpBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

type headlessBench struct {
	firstRun bool
}

func (b *headlessBench) id() string {
	if b.firstRun {
		return "launch.headless-init.first-run"
	}
	return "launch.headless-init.steady"
}

func (b *headlessBench) Spec() bench.Spec {
	profile := "steady"
	purpose := "Full runtime construction on the headless path with an existing " +
		"isolated home + workdir (primed once, untimed): config load, skills " +
		"catalog, repo root, memory store, provider, tools, policy/sandbox " +
		"executor, agents — then deliberate local exit at agent validation " +
		"(exit 1 BY DESIGN). Every measured iteration shares identical " +
		"preconditions; no setup work runs inside the timed region."
	if b.firstRun {
		profile = "first-run"
		purpose = "Same headless workload with a FRESH isolated home + workdir " +
			"per iteration: config.Dir creates+chmods ~/.forcefield, " +
			"config.Load writes config.yaml and prints \"Created default " +
			"config\" to stderr. No warm-up. This is NOT an OS-cold " +
			"measurement: first-run means fresh home, never a cold file cache."
	}
	spec := bench.Spec{
		ID:                b.id(),
		DefinitionVersion: 1,
		Title:             "Headless runtime init (" + profile + ")",
		Purpose:           purpose,
		Kind:              bench.KindE2E,
		Tier:              1,
		Metrics:           launchMetrics(),
		Params: map[string]string{
			"workload": "ff run --agent __bench_bogus__ x",
			"profile":  profile, "repo": "none", "mcp_servers": "0",
		},
		Predicate: "exit_code==1; pre/post probe: unknown-agent error and stage-agents seen",
	}
	if b.firstRun {
		spec.Plans = map[string]bench.Plan{
			"quick":    {Warmup: 0, N: 10, TimeoutSec: 60},
			"standard": {Warmup: 0, N: 30, TimeoutSec: 60},
			"full":     {Warmup: 0, N: 100, TimeoutSec: 120},
		}
	}
	return spec
}

func (b *headlessBench) Setup(ctx context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	parent, err := launchParent(env.Root, b.id(), subj)
	if err != nil {
		return bench.Fixture{}, err
	}
	timeout := planTimeout(b.Spec(), env.Profile)
	if b.firstRun {
		// No priming: every iteration (and probe) builds its own
		// fresh pair under the parent.
		return bench.Fixture{HomeDir: parent, WorkDir: parent, Timeout: timeout}, nil
	}
	home, work, err := freshDirs(parent, "shared")
	if err != nil {
		return bench.Fixture{}, err
	}
	// Untimed priming run: generates the real default config.yaml so
	// measured iterations start from identical steady preconditions.
	if err := primeHeadlessHome(ctx, subj.Path, home, work, timeout); err != nil {
		return bench.Fixture{}, err
	}
	return bench.Fixture{HomeDir: home, WorkDir: work, Timeout: timeout}, nil
}

// primeHeadlessHome runs the headless workload once, untimed, to
// generate the real default config.yaml. The workload exits 1 by
// design; anything else means the subject does not reach the
// intended boundary.
func primeHeadlessHome(ctx context.Context, subjPath, home, work string, timeout time.Duration) error {
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subjPath, Args: headlessArgs(),
		Env: launchEnv(home, false), Dir: work, Timeout: timeout,
	})
	if err != nil {
		return fmt.Errorf("priming run: %w", err)
	}
	if res.ExitCode != 1 || res.TimedOut {
		return &bench.SkipError{
			Status: schema.StatusInvalid,
			Code:   schema.ErrInvalidWorkload,
			Detail: fmt.Sprintf("priming: exit=%d timed_out=%v, want the unknown-agent exit 1",
				res.ExitCode, res.TimedOut),
		}
	}
	return nil
}

func (b *headlessBench) Iterate(ctx context.Context, fx bench.Fixture, subj bench.Subject, it bench.Iter) (bench.Observation, error) {
	home, work := fx.HomeDir, fx.WorkDir
	if b.firstRun {
		// Fresh pair per iteration, created OUTSIDE the timed
		// region: spawn.Run starts its clock at process start.
		var err error
		home, work, err = freshDirs(fx.HomeDir, itoa(uint64(it.Index)))
		if err != nil {
			return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
		}
	}
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subj.Path, Args: headlessArgs(),
		Env: launchEnv(home, false), Dir: work, Timeout: fx.Timeout,
	})
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	obs := bench.Observation{
		Values: map[string]float64{"wall_ms": res.WallMS, "cpu_ms": cpuMs(res)},
		// Exit 1 is expected application termination (unknown agent
		// after the full runtime was built), not a failure.
		Valid: res.ExitCode == 1 && !res.TimedOut,
		Attrs: exitAttr(res),
	}
	if !obs.Valid {
		obs.InvalidReason = "expected exit_code==1 (unknown-agent termination)"
	}
	return obs, nil
}

// Probe runs the markers-on confirmation: the workload must still
// reach agent validation (exit 1), print the unknown-agent error,
// and emit stage-agents. Guards against validation moving earlier in
// a future release, which would silently measure something else.
func (b *headlessBench) Probe(ctx context.Context, fx bench.Fixture, subj bench.Subject) (bool, map[string]bool, error) {
	home, work := fx.HomeDir, fx.WorkDir
	if b.firstRun {
		var err error
		home, work, err = freshDirs(fx.HomeDir, "probe")
		if err != nil {
			return false, nil, err
		}
	}
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subj.Path, Args: headlessArgs(),
		Env: launchEnv(home, true), Dir: work,
		CaptureStderr: true, Timeout: fx.Timeout,
	})
	checks := map[string]bool{}
	if err != nil {
		return false, checks, err
	}
	checks["exit_code_1"] = res.ExitCode == 1 && !res.TimedOut
	checks["unknown_agent_error"] = strings.Contains(res.Stderr, `unknown agent "`+sentinelAgent+`"`)
	checks["stage_agents_seen"] = markers.Has(res.Stderr, "stage-agents")
	ok := checks["exit_code_1"] && checks["unknown_agent_error"] && checks["stage_agents_seen"]
	return ok, checks, nil
}

func (b *headlessBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

type artifactSizeBench struct{}

func (b *artifactSizeBench) Spec() bench.Spec {
	return bench.Spec{
		ID:                "launch.artifact-size",
		DefinitionVersion: 1,
		Title:             "Release artifact size",
		Purpose: "Exact subject executable size via stat() + sha256. Deterministic " +
			"complement to timing; repeated for equality. Executable size is " +
			"not memory usage. Comparable across releases only for " +
			"like-for-like builds (recorded build flags must match).",
		Kind: bench.KindStatic,
		Tier: 1,
		Metrics: []bench.MetricSpec{
			{Name: "size_bytes", Unit: "bytes", Direction: bench.LowerIsBetter},
		},
		Params:    map[string]string{"what": "subject executable"},
		Predicate: "stat succeeds; sha256 recorded per sample",
		Plans: map[string]bench.Plan{
			"quick":    {Warmup: 0, N: 1, TimeoutSec: 60},
			"standard": {Warmup: 0, N: 3, TimeoutSec: 60},
			"full":     {Warmup: 0, N: 3, TimeoutSec: 60},
		},
	}
}

func (b *artifactSizeBench) Setup(_ context.Context, _ *bench.RunEnv, _ bench.Subject) (bench.Fixture, error) {
	return bench.Fixture{}, nil
}

func (b *artifactSizeBench) Iterate(_ context.Context, _ bench.Fixture, subj bench.Subject, _ bench.Iter) (bench.Observation, error) {
	st, err := os.Stat(subj.Path)
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	sum, err := sha256File(subj.Path)
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	return bench.Observation{
		Values: map[string]float64{"size_bytes": float64(st.Size())},
		Valid:  true,
		Attrs:  map[string]string{"sha256": sum},
	}, nil
}

func (b *artifactSizeBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

// planTimeout resolves the spawn budget for a profile.
func planTimeout(spec bench.Spec, profile string) time.Duration {
	return time.Duration(spec.PlanFor(profile).TimeoutSec) * time.Second
}

func exitAttr(res spawn.Result) map[string]string {
	return map[string]string{"exit_code": itoa(uint64(res.ExitCode))}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
