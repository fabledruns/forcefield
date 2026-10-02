// Package runner executes HPOV benchmarks: fixture setup, warm-up,
// interleaved measurement, marker probes, statistics, quality flags,
// streaming JSONL plus an atomic canonical result.
package runner

import (
	"bufio"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/envinfo"
	"forcefield/internal/hpov/fixture"
	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/stats"
	"forcefield/internal/hpov/subject"
)

// Config controls one hpov run.
type Config struct {
	Benchmarks    []bench.Benchmark
	Subjects      []bench.Subject
	SubjectSource string // "release" | "local-build"
	Select        []string
	Exclude       []string
	Tier          int // negative = all tiers
	Kind          string
	Profile       string
	N             *int
	Warmup        *int
	Seed          int64
	Out           string
	WorkRoot      string
	FailFast      bool
	CommandLine   []string
}

// Outcome summarizes a finished run.
type Outcome struct {
	Result          *schema.Result
	Path            string
	BenchmarkErrors bool
}

// Available reports requirement availability on this host.
func Available() map[string]bool {
	have := map[string]bool{}
	have["go"] = lookOK("go")
	have["git"] = lookOK("git")
	have["rg"] = lookOK("rg")
	have["pty"] = havePTY()
	have["markers"] = true // parse capability; subject support is probed
	return have
}

func lookOK(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Run executes the selected benchmarks and writes the result file.
func Run(ctx context.Context, cfg Config) (Outcome, error) {
	profile := cfg.Profile
	if profile == "" {
		profile = "standard"
	}
	if _, ok := bench.StandardPlans[profile]; !ok {
		return Outcome{}, fmt.Errorf("unknown profile %q (quick|standard|full)", profile)
	}
	selected := selectBenchmarks(cfg)
	if len(selected) == 0 {
		return Outcome{}, fmt.Errorf("no benchmarks selected")
	}
	probed := make([]subject.ProbeResult, 0, len(cfg.Subjects))
	for _, s := range cfg.Subjects {
		pr, err := subject.Probe(s.Label, s.Path, cfg.SubjectSource)
		if err != nil {
			return Outcome{}, err
		}
		probed = append(probed, pr)
	}
	root, err := fixture.NewRunRoot(cfg.WorkRoot)
	if err != nil {
		return Outcome{}, err
	}
	info, err := envinfo.Collect(root)
	if err != nil {
		return Outcome{}, err
	}
	_, removed := fixture.ScrubEnv(nil)

	runID := newRunID()
	started := time.Now().UTC()
	rng := rand.New(rand.NewPCG(uint64(cfg.Seed), 0x9E3779B97F4A7C15))

	partialPath := cfg.Out + ".partial.jsonl"
	if cfg.Out == "" {
		return Outcome{}, fmt.Errorf("--out is required")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Out), 0o755); err != nil {
		return Outcome{}, fmt.Errorf("create out dir: %w", err)
	}
	pf, err := os.Create(partialPath)
	if err != nil {
		return Outcome{}, fmt.Errorf("create partial log: %w", err)
	}
	pw := bufio.NewWriter(pf)
	emit := func(ev map[string]any) {
		raw, _ := json.Marshal(ev)
		_, _ = pw.Write(raw)
		_ = pw.WriteByte('\n')
		_ = pw.Flush()
	}
	emit(map[string]any{"event": "run.start", "id": runID, "profile": profile})
	defer func() {
		_ = pw.Flush()
		_ = pf.Close()
	}()

	res := &schema.Result{
		Schema:        schema.SchemaName,
		SchemaVersion: schema.SchemaVersion,
		Suite:         schema.Suite{Name: "hpov", Version: schema.SuiteVersion, Profile: profile, DefinitionSet: schema.DefinitionSet},
		Run: schema.Run{
			ID:             runID,
			StartedAt:      started.Format(time.RFC3339),
			CommandLine:    cfg.CommandLine,
			Seed:           cfg.Seed,
			QuantileMethod: schema.QuantileMethod,
			Bootstrap:      schema.Bootstrap{Resamples: schema.BootstrapResamples, Seed: cfg.Seed, Method: "percentile"},
		},
		Host: info.Host,
		Environment: schema.Environment{
			HomeIsolated:    true,
			Stdin:           "null",
			MarkersWallPass: "off",
			EnvOverrides: schema.EnvOverrides{
				Removed: removed,
				Set:     map[string]string{"HOME|USERPROFILE": "<isolated-home>", "FF_PERF_MARKERS": "1 (marker pass only)"},
			},
		},
	}

	for _, pr := range probed {
		res.Subjects = append(res.Subjects, pr.Subject)
	}

	// Spawn-floor calibration brackets the run; it quantifies runner
	// overhead and drift and is never subtracted.
	calStart, calWarn := calibrate(ctx, cfg.Seed)
	res.Environment.Calibration.SpawnFloorMs.Start = calStart

	runEnv := &bench.RunEnv{Root: root, Profile: profile, Seed: cfg.Seed}
	benchmarkErrors := false
	for _, b := range selected {
		spec := b.Spec()
		entries := runOne(ctx, b, spec, probed, runEnv, cfg, rng, emit)
		for _, e := range entries {
			if e.Status == schema.StatusError || e.Status == schema.StatusInvalid {
				benchmarkErrors = true
			}
			res.Benchmarks = append(res.Benchmarks, e)
		}
		if cfg.FailFast && benchmarkErrors {
			break
		}
	}

	calEnd, calWarn2 := calibrate(ctx, cfg.Seed+1)
	res.Environment.Calibration.SpawnFloorMs.End = calEnd
	calWarn = append(calWarn, calWarn2...)

	res.Environment.Quality = assessQuality(info.IdleCPUPct, calStart, calEnd, res.Benchmarks)
	res.Run.Quality = schema.RunQuality{Label: qualityLabel(res.Environment.Quality, res.Benchmarks)}
	for _, w := range calWarn {
		res.Warnings = append(res.Warnings, w)
	}
	for _, f := range res.Environment.Quality.Flags {
		res.Run.Quality.Flags = append(res.Run.Quality.Flags, f)
	}
	res.Run.FinishedAt = time.Now().UTC().Format(time.RFC3339)

	emit(map[string]any{"event": "run.end", "id": runID})
	_ = pw.Flush()
	_ = pf.Close()

	if err := writeAtomic(cfg.Out, res); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: res, Path: cfg.Out, BenchmarkErrors: benchmarkErrors}, nil
}

func selectBenchmarks(cfg Config) []bench.Benchmark {
	var out []bench.Benchmark
	for _, b := range cfg.Benchmarks {
		spec := b.Spec()
		if !bench.MatchAny(cfg.Select, spec.ID) {
			continue
		}
		if len(cfg.Exclude) > 0 && bench.MatchAny(cfg.Exclude, spec.ID) {
			continue
		}
		if len(cfg.Select) == 0 {
			if cfg.Tier >= 0 && spec.Tier != cfg.Tier {
				continue
			}
			if cfg.Kind != "" && string(spec.Kind) != cfg.Kind {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

// subjectNeeder is implemented by benchmarks that run without a
// subject binary (infrastructure like calibration.noop).
type subjectNeeder interface{ NeedsSubject() bool }

func needsSubject(b bench.Benchmark) bool {
	if n, ok := b.(subjectNeeder); ok {
		return n.NeedsSubject()
	}
	return true
}

// runOne executes one benchmark for all its subjects (one entry per
// subject). Warm-ups run subject-major; measures interleave
// round-robin with a seeded per-round order.
func runOne(ctx context.Context, b bench.Benchmark, spec bench.Spec, probed []subject.ProbeResult,
	runEnv *bench.RunEnv, cfg Config, rng *rand.Rand, emit func(map[string]any)) []schema.Benchmark {
	plan := spec.PlanFor(runEnv.Profile)
	if cfg.N != nil {
		plan.N = *cfg.N
	}
	if cfg.Warmup != nil {
		plan.Warmup = *cfg.Warmup
	}
	timeout := time.Duration(plan.TimeoutSec) * time.Second

	type work struct {
		subj     bench.Subject
		fx       bench.Fixture
		setupErr error
		skip     *bench.SkipError
		pre      *schema.Probe
		post     *schema.Probe
		iters    []schema.Iteration
		obs      []bench.Observation // measure-phase, in order
		t0       time.Time
		idx      int
	}
	var works []*work
	if !needsSubject(b) {
		works = append(works, &work{subj: bench.Subject{}})
	} else if len(probed) == 0 {
		d := "no subject: pass --ff label=path"
		return []schema.Benchmark{{
			ID: spec.ID, DefinitionVersion: spec.DefinitionVersion,
			Tier: spec.Tier, Kind: string(spec.Kind),
			Status: schema.StatusSkipped, StatusDetail: &d,
			Params: spec.Params, Plan: schema.PlanOut{Warmup: plan.Warmup, N: plan.N},
		}}
	} else {
		for _, p := range probed {
			works = append(works, &work{subj: bench.Subject{Label: p.Label, Path: p.Path}})
		}
	}

	have := Available()
	entryFor := func(w *work, spec bench.Spec) schema.Benchmark {
		e := schema.Benchmark{
			ID: spec.ID, DefinitionVersion: spec.DefinitionVersion,
			Tier: spec.Tier, Kind: string(spec.Kind),
			Params: spec.Params,
			Plan:   schema.PlanOut{Warmup: plan.Warmup, N: plan.N, Interleaved: len(works) > 1},
		}
		if w.subj.Label != "" {
			e.Subject = w.subj.Label
		}
		return e
	}

	// Host gate first (same for every subject).
	if ok, reason := spec.RunnableOn(hostGOOS(), have); !ok {
		status := schema.StatusSkipped
		if reason == "unsupported_platform" {
			status = schema.StatusUnsupported
		}
		var out []schema.Benchmark
		for _, w := range works {
			e := entryFor(w, spec)
			e.Status = status
			d := reason
			e.StatusDetail = &d
			out = append(out, e)
		}
		return out
	}

	for _, w := range works {
		emit(map[string]any{"event": "benchmark.start", "id": spec.ID, "subject": w.subj.Label})
		fx, err := b.Setup(ctx, runEnv, w.subj)
		if err != nil {
			if se, ok := err.(*bench.SkipError); ok {
				w.skip = se
			} else {
				w.setupErr = err
			}
			continue
		}
		w.fx = fx
		w.t0 = time.Now().UTC()
		if p, ok := b.(bench.Prober); ok {
			w.pre = runProbe(ctx, p, fx, w.subj, timeout)
		}
	}

	active := func() []*work {
		var a []*work
		for _, w := range works {
			if w.skip == nil && w.setupErr == nil {
				a = append(a, w)
			}
		}
		return a
	}

	// Warm-up: subject-major, stored but never in statistics.
	for _, w := range active() {
		for i := 0; i < plan.Warmup; i++ {
			it := bench.Iter{Index: w.idx, Phase: bench.PhaseWarmup}
			w.idx++
			obs, oerr := b.Iterate(ctx, w.fx, w.subj, it)
			w.iters = append(w.iters, toIteration(it, w.t0, obs, oerr))
			emit(map[string]any{"event": "iteration", "id": spec.ID, "subject": w.subj.Label, "phase": bench.PhaseWarmup})
		}
	}
	// Measures: interleaved round-robin, seeded order per round.
	for r := 0; r < plan.N; r++ {
		order := active()
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, w := range order {
			it := bench.Iter{Index: w.idx, Phase: bench.PhaseMeasure}
			w.idx++
			obs, oerr := b.Iterate(ctx, w.fx, w.subj, it)
			w.iters = append(w.iters, toIteration(it, w.t0, obs, oerr))
			w.obs = append(w.obs, normalize(obs, oerr, spec))
			emit(map[string]any{"event": "iteration", "id": spec.ID, "subject": w.subj.Label, "phase": bench.PhaseMeasure})
		}
	}

	var out []schema.Benchmark
	for _, w := range works {
		e := entryFor(w, spec)
		switch {
		case w.skip != nil:
			e.Status = w.skip.Status
			e.StatusDetail = &w.skip.Detail
			if w.skip.Code != "" {
				e.Error = &schema.BenchError{Code: w.skip.Code, Phase: "setup", Message: w.skip.Detail}
			}
		case w.setupErr != nil:
			e.Status = schema.StatusError
			e.Error = &schema.BenchError{Code: schema.ErrSetupFailed, Phase: "setup", Message: w.setupErr.Error()}
		default:
			if p, ok := b.(bench.Prober); ok {
				w.post = runProbe(ctx, p, w.fx, w.subj, timeout)
				e.Validity = &schema.Validity{
					Predicate: spec.Predicate,
					ProbePre:  w.pre, ProbePost: w.post,
				}
			}
			if err := b.Teardown(ctx, w.fx); err != nil {
				e.Warnings = append(e.Warnings, "teardown: "+err.Error())
			}
			e.Iterations = w.iters
			e.Metrics = buildMetrics(spec, w.obs, cfg.Seed)
			probesOK := w.pre == nil || (w.pre.OK && (w.post == nil || w.post.OK))
			if !probesOK {
				e.Status = schema.StatusInvalid
				d := "validity probe failed: workload boundary unconfirmed"
				e.StatusDetail = &d
				for i := range e.Metrics {
					e.Metrics[i].Statistics = nil
				}
			} else if allInvalid(e.Metrics) {
				e.Status = schema.StatusInvalid
				d := "no valid samples"
				e.StatusDetail = &d
			} else {
				e.Status = schema.StatusOK
			}
			e.Flags = metricFlags(spec, w.obs, e.Metrics)
		}
		emit(map[string]any{"event": "benchmark.end", "id": spec.ID, "subject": w.subj.Label, "status": e.Status})
		out = append(out, e)
	}
	return out
}

func toIteration(it bench.Iter, t0 time.Time, obs bench.Observation, oerr error) schema.Iteration {
	// t_offset_ms anchors at the first timed iteration spawn; here we
	// use call time (suites spawn immediately after).
	out := schema.Iteration{
		I:         it.Index,
		Phase:     it.Phase,
		Valid:     obs.Valid,
		TOffsetMs: round3(float64(time.Now().UTC().Sub(t0).Nanoseconds()) / 1e6),
		Attrs:     obs.Attrs,
		Values:    obs.Values,
	}
	if oerr != nil {
		out.Valid = false
		if out.Attrs == nil {
			out.Attrs = map[string]string{}
		}
		out.Attrs["iterate_error"] = oerr.Error()
	}
	return out
}

// normalize forces missing owned metrics to invalid zeros so values
// arrays stay aligned with measure iterations.
func normalize(obs bench.Observation, oerr error, spec bench.Spec) bench.Observation {
	if oerr != nil {
		obs.Valid = false
		obs.InvalidReason = oerr.Error()
	}
	if obs.Values == nil {
		obs.Values = map[string]float64{}
	}
	if obs.Attrs == nil {
		obs.Attrs = map[string]string{}
	}
	if !obs.Valid && obs.InvalidReason == "" {
		obs.InvalidReason = "invalid"
	}
	for _, m := range spec.Metrics {
		if _, ok := obs.Values[m.Name]; !ok {
			obs.Values[m.Name] = 0
			obs.Valid = false
			if obs.InvalidReason == "" {
				obs.InvalidReason = "missing metric " + m.Name
			}
		}
	}
	return obs
}

func runProbe(ctx context.Context, p bench.Prober, fx bench.Fixture, subj bench.Subject, timeout time.Duration) *schema.Probe {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ok, checks, err := p.Probe(cctx, fx, subj)
	pr := &schema.Probe{OK: ok && err == nil, Checks: checks}
	if err != nil {
		if pr.Checks == nil {
			pr.Checks = map[string]bool{}
		}
		pr.Checks["probe_error"] = false
	}
	return pr
}

func buildMetrics(spec bench.Spec, obs []bench.Observation, seed int64) []schema.Metric {
	out := make([]schema.Metric, 0, len(spec.Metrics))
	for _, m := range spec.Metrics {
		vals := make([]float64, 0, len(obs))
		valid := make([]bool, 0, len(obs))
		for _, o := range obs {
			vals = append(vals, o.Values[m.Name])
			valid = append(valid, o.Valid)
		}
		sm := stats.Summarize(vals, valid, seed)
		var st *schema.Statistics
		if sm != nil && sm.ValidN > 0 {
			st = &schema.Statistics{
				N: sm.N, ValidN: sm.ValidN, Min: sm.Min, P50: sm.P50,
				P90: sm.P90, P95: sm.P95, P99: sm.P99, Max: sm.Max,
				Mean: sm.Mean, Stdev: sm.Stdev, MAD: sm.MAD, IQR: sm.IQR,
				RobustCV: sm.RobustCV, CI95P50: sm.CI95P50, Nulls: sm.Nulls,
			}
		}
		me := schema.Metric{Name: m.Name, Unit: m.Unit, Direction: m.Direction, Values: vals, Statistics: st}
		if s, ok := m.PlatformSemantics[hostGOOS()]; ok {
			me.PlatformSemantics = s
		}
		out = append(out, me)
	}
	return out
}

func allInvalid(metrics []schema.Metric) bool {
	if len(metrics) == 0 {
		return true
	}
	for _, m := range metrics {
		if m.Statistics != nil && m.Statistics.ValidN > 0 {
			return false
		}
	}
	return true
}

// metricFlags applies noisy/bimodal/drift detectors per metric.
func metricFlags(spec bench.Spec, obs []bench.Observation, metrics []schema.Metric) []string {
	thr := spec.CVThreshold
	if thr <= 0 {
		thr = 0.10
	}
	var flags []string
	for _, m := range metrics {
		if m.Statistics == nil || m.Statistics.ValidN == 0 {
			continue
		}
		cv := m.Statistics.RobustCV
		if cv == cv && cv > thr { // NaN-safe
			flags = append(flags, "noisy:"+m.Name)
		}
		vals := validValues(obs, m.Name)
		sorted := append([]float64(nil), vals...)
		sort.Float64s(sorted)
		if stats.Bimodal(sorted, m.Statistics.P50, m.Statistics.MAD) {
			flags = append(flags, "bimodal:"+m.Name)
		}
		if stats.DriftFlagged(vals) {
			flags = append(flags, "drift:"+m.Name)
		}
	}
	return flags
}

func validValues(obs []bench.Observation, metric string) []float64 {
	var out []float64
	for _, o := range obs {
		if o.Valid {
			out = append(out, o.Values[metric])
		}
	}
	return out
}

func assessQuality(idle *float64, start, end schema.CalibrationPoint, benches []schema.Benchmark) schema.EnvQuality {
	var q schema.EnvQuality
	q.IdleCPUPct = idle
	if idle != nil && *idle > 10 {
		q.Flags = append(q.Flags, "high_idle_cpu")
	}
	if start.P50 > 0 {
		d := (end.P50 - start.P50) / start.P50 * 100
		if d < 0 {
			d = -d
		}
		q.DriftSpawnFloorPct = &d
		if d > 25 {
			q.Flags = append(q.Flags, "spawn_floor_drift")
		}
	}
	noisy, total := 0, 0
	for _, b := range benches {
		if b.Status != schema.StatusOK {
			continue
		}
		total++
		for _, f := range b.Flags {
			if len(f) >= 6 && f[:6] == "noisy:" {
				noisy++
				break
			}
		}
		for _, f := range b.Flags {
			if len(f) >= 6 && (f[:6] == "drift:" || f[:6] == "bimoda"[:6]) {
				q.Flags = append(q.Flags, b.ID+":"+f)
			}
		}
	}
	if total > 0 && float64(noisy)/float64(total) > 0.3 {
		q.Flags = append(q.Flags, "many_noisy_benchmarks")
	}
	return q
}

func qualityLabel(q schema.EnvQuality, benches []schema.Benchmark) string {
	for _, f := range q.Flags {
		if f == "spawn_floor_drift" || f == "many_noisy_benchmarks" {
			return "poor"
		}
	}
	if len(q.Flags) > 0 {
		return "degraded"
	}
	for _, b := range benches {
		if len(b.Flags) > 0 {
			return "degraded"
		}
	}
	return "good"
}

func writeAtomic(path string, res *schema.Result) error {
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("write temp result: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publish result: %w", err)
	}
	return nil
}

func newRunID() string {
	var b [4]byte
	_, _ = crand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

func round3(f float64) float64 {
	if f != f {
		return 0
	}
	return float64(int64(f*1000+0.5)) / 1000
}

// hostGOOS is a seam for tests.
var hostGOOS = func() string { return runtime.GOOS }
