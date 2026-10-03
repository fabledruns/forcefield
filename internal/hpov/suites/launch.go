// Launch benchmarks: process-level end-to-end measurement of a subject
// executable using only externally observable boundaries (spawn, exit,
// exit code, stdout/stderr text, markers on a separate pass).
//
// Every command, output predicate and exit status comes from the
// subject's contract; nothing here assumes a particular harness. A
// subject whose contract omits a workload gets an explicit unsupported
// entry rather than a measurement of something else.
//
// Contracts:
//
//	launch.version — the contract's version command in an isolated
//	  workdir. Start: immediately before process start. End: Wait
//	  returns. Validity: the contract's exit status and stdout prefix.
//	  Metrics: wall_ms (primary), cpu_ms = user+sys (secondary).
//
//	launch.help — the contract's help command, same boundaries, with the
//	  contract's stdout predicate. Help byte count recorded per
//	  iteration (command-set drift changes help size).
//
//	launch.headless-init.steady — the contract's headless workload with
//	  an existing isolated home + workdir, primed once untimed so every
//	  measured iteration shares identical preconditions. The workload
//	  reaches its intended boundary by a deliberate non-zero exit.
//	  Validity per sample: the contract's exit status. Per run: the
//	  pre/post probe must satisfy every contract probe check.
//	  Metrics: wall_ms, cpu_ms.
//
//	launch.headless-init.first-run — same workload, fresh isolated
//	  home + workdir per iteration. No warm-up. This is NOT an OS-cold
//	  measurement: "first-run" means fresh home, never a cold file
//	  cache.
//
//	launch.artifact-size — stat() + sha256 of the subject binary.
//	  Exact byte size, deterministic; executable size is not memory
//	  usage. Repeated 3x for equality. Subject-independent: it needs no
//	  contract at all.
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

// launchEnv builds the scrubbed child environment with home isolation
// (USERPROFILE on Windows, HOME on Unix). markers turns the subject's
// own instrumentation on for the marker pass only; the wall pass never
// sets it, so instrumentation perturbation is never inside headline
// samples. Both the marker variables and the subject's scrub list come
// from the contract, so a subject with neither is unaffected.
func launchEnv(subj bench.Subject, home string, withMarkers bool) []string {
	set := fixture.HomeEnv(home)
	if withMarkers {
		for k, v := range subj.Contract.EnableEnv {
			set[k] = v
		}
	}
	for k, v := range subj.Contract.Env.Passthrough {
		set[k] = v
	}
	env, _ := fixture.ScrubEnv(set, subj.Contract.Env)
	return env
}

// cpuAttr splits kernel CPU time for the secondary metric.
func cpuMs(res spawn.Result) float64 {
	return res.UserMS + res.SysMS
}

type versionBench struct{}

func (b *versionBench) Spec() bench.Spec {
	return b.SpecFor(bench.Subject{})
}

// SpecFor describes the benchmark against the subject's contract: the
// version command and its stdout predicate are the subject's, not HPOV's.
func (b *versionBench) SpecFor(subj bench.Subject) bench.Spec {
	c := subj.Contract
	return bench.Spec{
		ID:                "launch.version",
		DefinitionVersion: 1,
		Title:             "CLI floor: version command",
		Purpose: "OS process creation + runtime/package init + argument parsing of " +
			"the subject's version command. Not headline startup speed: it " +
			"calibrates how much of every other launch number is fixed cost. " +
			"Validity is the contract's exit status and stdout predicate.",
		Kind:      bench.KindE2E,
		Tier:      1,
		Metrics:   launchMetrics(),
		Params:    map[string]string{"workload": c.Method(c.Version.Args), "profile": "steady"},
		Predicate: c.Version.Predicate(),
	}
}

func launchMetrics() []bench.MetricSpec {
	return []bench.MetricSpec{
		{Name: "wall_ms", Unit: "ms", Direction: bench.LowerIsBetter},
		{Name: "cpu_ms", Unit: "ms", Direction: bench.LowerIsBetter},
	}
}

func (b *versionBench) Setup(_ context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	if !subj.Contract.Version.Defined() {
		return bench.Fixture{}, subj.Unsupported("a version command")
	}
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
	probe := subj.Contract.Version
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subj.Path, Args: probe.Args,
		Env: launchEnv(subj, fx.HomeDir, false), Dir: fx.WorkDir,
		CaptureStdout: true, Timeout: fx.Timeout,
	})
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	first := strings.TrimSpace(strings.SplitN(res.Stdout, "\n", 2)[0])
	obs := bench.Observation{
		Values: map[string]float64{"wall_ms": res.WallMS, "cpu_ms": cpuMs(res)},
		Valid:  res.ExitCode == probe.WantExit() && !res.TimedOut && stdoutOK(first, res.Stdout, probe),
		Attrs: map[string]string{
			"exit_code":    itoa(uint64(res.ExitCode)),
			"stdout_bytes": itoa(uint64(len(res.Stdout))),
			"version":      truncate(first, 80),
		},
	}
	if !obs.Valid {
		obs.InvalidReason = "expected " + probe.Predicate()
	}
	return obs, nil
}

// stdoutOK applies the contract's stdout predicate: a prefix on the first
// line, a substring anywhere, or nothing beyond the exit status.
func stdoutOK(first, all string, probe bench.Probe) bool {
	switch {
	case probe.StdoutPrefix != "":
		return strings.HasPrefix(first, probe.StdoutPrefix)
	case probe.StdoutContains != "":
		return strings.Contains(all, probe.StdoutContains)
	default:
		return true
	}
}

func (b *versionBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

type helpBench struct{}

func (b *helpBench) Spec() bench.Spec { return b.SpecFor(bench.Subject{}) }

func (b *helpBench) SpecFor(subj bench.Subject) bench.Spec {
	c := subj.Contract
	return bench.Spec{
		ID:                "launch.help",
		DefinitionVersion: 1,
		Title:             "CLI floor: help command",
		Purpose: "Same floor as the version command plus help rendering. " +
			"Help byte count is recorded per iteration: command-set drift " +
			"changes help size.",
		Kind:      bench.KindE2E,
		Tier:      1,
		Metrics:   launchMetrics(),
		Params:    map[string]string{"workload": c.Method(c.Help.Args), "profile": "steady"},
		Predicate: c.Help.Predicate(),
	}
}

func (b *helpBench) Setup(_ context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	if !subj.Contract.Help.Defined() {
		return bench.Fixture{}, subj.Unsupported("a help command")
	}
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
	probe := subj.Contract.Help
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subj.Path, Args: probe.Args,
		Env: launchEnv(subj, fx.HomeDir, false), Dir: fx.WorkDir,
		CaptureStdout: true, Timeout: fx.Timeout,
	})
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	obs := bench.Observation{
		Values: map[string]float64{"wall_ms": res.WallMS, "cpu_ms": cpuMs(res)},
		Valid: res.ExitCode == probe.WantExit() && !res.TimedOut &&
			stdoutOK("", res.Stdout, probe),
		Attrs: map[string]string{
			"exit_code":    itoa(uint64(res.ExitCode)),
			"stdout_bytes": itoa(uint64(len(res.Stdout))),
		},
	}
	if !obs.Valid {
		obs.InvalidReason = "expected " + probe.Predicate()
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

func (b *headlessBench) Spec() bench.Spec { return b.SpecFor(bench.Subject{}) }

func (b *headlessBench) SpecFor(subj bench.Subject) bench.Spec {
	c := subj.Contract
	profile := "steady"
	purpose := "Full subject initialization on the headless path with an existing " +
		"isolated home + workdir (primed once, untimed), measured to the " +
		"contract's deliberate boundary exit. Every measured iteration shares " +
		"identical preconditions; no setup work runs inside the timed region."
	if b.firstRun {
		profile = "first-run"
		purpose = "Same headless workload with a FRESH isolated home + workdir " +
			"per iteration, including whatever first-run state the subject " +
			"creates. No warm-up. This is NOT an OS-cold measurement: first-run " +
			"means fresh home, never a cold file cache."
	}
	spec := bench.Spec{
		ID:                b.id(),
		DefinitionVersion: 1,
		Title:             "Headless init (" + profile + ")",
		Purpose:           purpose,
		Kind:              bench.KindE2E,
		Tier:              1,
		Metrics:           launchMetrics(),
		Params: map[string]string{
			"workload": c.Method(c.Headless.Args),
			"profile":  profile, "repo": "none", "mcp_servers": "0",
		},
		Predicate: c.Headless.Predicate(),
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
	if !subj.Contract.Headless.Defined() {
		return bench.Fixture{}, subj.Unsupported("a headless workload")
	}
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
	// Untimed priming run when the contract asks for one: it creates the
	// subject's first-run state so measured iterations start from
	// identical steady preconditions.
	if subj.Contract.Headless.Primed {
		if err := primeHeadlessHome(ctx, subj, home, work, timeout); err != nil {
			return bench.Fixture{}, err
		}
	}
	return bench.Fixture{HomeDir: home, WorkDir: work, Timeout: timeout}, nil
}

// primeHeadlessHome runs the headless workload once, untimed, to create
// the subject's first-run state. The workload reaches its boundary by
// the contract's exit status; anything else means the subject does not
// reach the intended boundary.
func primeHeadlessHome(ctx context.Context, subj bench.Subject, home, work string, timeout time.Duration) error {
	c := subj.Contract.Headless
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subj.Path, Args: c.Args,
		Env: launchEnv(subj, home, false), Dir: work, Timeout: timeout,
	})
	if err != nil {
		return fmt.Errorf("priming run: %w", err)
	}
	if res.ExitCode != c.ExitCode || res.TimedOut {
		return &bench.SkipError{
			Status: schema.StatusInvalid,
			Code:   schema.ErrInvalidWorkload,
			Detail: fmt.Sprintf("priming: exit=%d timed_out=%v, want the contract's boundary exit %d",
				res.ExitCode, res.TimedOut, c.ExitCode),
		}
	}
	return nil
}

func (b *headlessBench) Iterate(ctx context.Context, fx bench.Fixture, subj bench.Subject, it bench.Iter) (bench.Observation, error) {
	c := subj.Contract.Headless
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
		Path: subj.Path, Args: c.Args,
		Env: launchEnv(subj, home, false), Dir: work, Timeout: fx.Timeout,
	})
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	obs := bench.Observation{
		Values: map[string]float64{"wall_ms": res.WallMS, "cpu_ms": cpuMs(res)},
		// The contract's boundary exit is expected application
		// termination, not a failure.
		Valid: res.ExitCode == c.ExitCode && !res.TimedOut,
		Attrs: exitAttr(res),
	}
	if !obs.Valid {
		obs.InvalidReason = fmt.Sprintf("expected the contract's boundary exit_code==%d", c.ExitCode)
	}
	return obs, nil
}

// Probe runs the markers-on confirmation: the workload must still reach
// the contract's boundary exit and satisfy every contract probe check
// (stderr text, or an instrumentation marker). It guards against a
// future release moving the boundary earlier, which would silently
// measure something else.
func (b *headlessBench) Probe(ctx context.Context, fx bench.Fixture, subj bench.Subject) (bool, map[string]bool, error) {
	c := subj.Contract.Headless
	home, work := fx.HomeDir, fx.WorkDir
	if b.firstRun {
		var err error
		home, work, err = freshDirs(fx.HomeDir, "probe")
		if err != nil {
			return false, nil, err
		}
	}
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subj.Path, Args: c.Args,
		Env: launchEnv(subj, home, true), Dir: work,
		CaptureStderr: true, Timeout: fx.Timeout,
	})
	checks := map[string]bool{}
	if err != nil {
		return false, checks, err
	}
	ok := res.ExitCode == c.ExitCode && !res.TimedOut
	checks["exit_code_"+itoa(uint64(c.ExitCode))] = ok
	proto := markers.Protocol{Prefix: subj.Contract.MarkerPrefix}
	for _, chk := range c.ProbeChecks {
		switch {
		case chk.Substring != "":
			checks[chk.Name] = strings.Contains(res.Stderr, chk.Substring)
		case chk.Marker != "":
			checks[chk.Name] = proto.Has(res.Stderr, chk.Marker)
		}
		ok = ok && checks[chk.Name]
	}
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
