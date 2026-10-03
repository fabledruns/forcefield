package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/subject"
)

// TestMain makes the test binary a valid __noop subject: the
// calibration path re-execs os.Executable with __noop.
func TestMain(m *testing.M) {
	for _, a := range os.Args[1:] {
		if a == "__noop" {
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// recordBench is a hermetic fake: deterministic values, order log.
type recordBench struct {
	mu    sync.Mutex
	order []string
	id    string
	tier  int
}

func (f *recordBench) Spec() bench.Spec {
	return bench.Spec{
		ID: "test.fake", DefinitionVersion: 1, Tier: f.tier,
		Kind:    bench.KindE2E,
		Metrics: []bench.MetricSpec{{Name: "x_ms", Unit: "ms", Direction: bench.LowerIsBetter}},
	}
}

func (f *recordBench) Setup(_ context.Context, _ *bench.RunEnv, _ bench.Subject) (bench.Fixture, error) {
	return bench.Fixture{}, nil
}

func (f *recordBench) Iterate(_ context.Context, _ bench.Fixture, subj bench.Subject, it bench.Iter) (bench.Observation, error) {
	f.mu.Lock()
	f.order = append(f.order, subj.Label+":"+it.Phase)
	f.mu.Unlock()
	return bench.Observation{
		Values: map[string]float64{"x_ms": 42},
		Valid:  true,
		Attrs:  map[string]string{"exit_code": "0"},
	}, nil
}

func (f *recordBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

func testSubjects(t *testing.T) []bench.Subject {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Probe only stats+hashes; a version-command failure is tolerated.
	return []bench.Subject{
		{Label: "base", Path: self, Contract: subject.Builtin().Contract},
		{Label: "head", Path: self, Contract: subject.Builtin().Contract},
	}
}

func runFake(t *testing.T, seed int64) ([]schema.Benchmark, []string) {
	t.Helper()
	fb := &recordBench{tier: 1}
	n, w := 4, 1
	out := filepath.Join(t.TempDir(), "r.json")
	oc, err := Run(context.Background(), Config{
		Benchmarks:     []bench.Benchmark{fb},
		Subjects:       testSubjects(t),
		SubjectProfile: subject.Builtin(),
		Select:         []string{"test.fake"},
		Tier:           -1,
		Profile:        "quick",
		N:              &n,
		Warmup:         &w,
		Seed:           seed,
		Out:            out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(oc.Result.Benchmarks) != 2 {
		t.Fatalf("want 2 entries (one per subject), got %d", len(oc.Result.Benchmarks))
	}
	// Subject provenance must be stored at run level.
	if len(oc.Result.Subjects) != 2 {
		t.Fatalf("want 2 stored subjects, got %d", len(oc.Result.Subjects))
	}
	for _, s := range oc.Result.Subjects {
		if s.Binary.SHA256 == "" || s.Binary.SizeBytes <= 0 {
			t.Fatalf("subject %s lacks provenance: %+v", s.Label, s.Binary)
		}
	}
	return oc.Result.Benchmarks, fb.order
}

func TestInterleaveDeterministic(t *testing.T) {
	b1, o1 := runFake(t, 424242)
	b2, o2 := runFake(t, 424242)
	if len(o1) != len(o2) {
		t.Fatalf("order lengths differ: %d vs %d", len(o1), len(o2))
	}
	for i := range o1 {
		if o1[i] != o2[i] {
			t.Fatalf("nondeterministic order at %d: %v vs %v", i, o1, o2)
		}
	}
	// Warm-ups are subject-major (all base warmup before head warmup
	// would also be acceptable); measures must interleave: in every
	// window of 2 consecutive measure iterations both subjects appear.
	measures := func(order []string) []string {
		var m []string
		for _, o := range order {
			if len(o) > 8 && o[len(o)-7:] == "measure" {
				m = append(m, o)
			}
		}
		return m
	}
	m := measures(o1)
	if len(m) != 8 {
		t.Fatalf("want 8 measure iterations, got %v", m)
	}
	for i := 0; i < len(m); i += 2 {
		if m[i][:4] == m[i+1][:4] {
			t.Fatalf("round %d not interleaved: %v", i/2, m)
		}
	}
	for _, b := range append(b1, b2...) {
		if b.Status != schema.StatusOK {
			t.Fatalf("%s status = %s", b.ID, b.Status)
		}
		if !b.Plan.Interleaved {
			t.Fatal("interleaved flag must be set for multi-subject runs")
		}
	}
}

func TestNoopEndToEnd(t *testing.T) {
	// Runs the real calibration.noop benchmark (spawn path, real
	// wall times) and validates the self-produced document.
	n, w := 2, 1
	out := filepath.Join(t.TempDir(), "r.json")
	oc, err := Run(context.Background(), Config{
		Benchmarks: []bench.Benchmark{NoopBenchmark("")},
		Select:     []string{"calibration.noop"},
		Tier:       -1,
		Profile:    "quick",
		N:          &n,
		Warmup:     &w,
		Seed:       7,
		Out:        out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(oc.Result.Benchmarks) != 1 || oc.Result.Benchmarks[0].Status != schema.StatusOK {
		t.Fatalf("noop entry: %+v", oc.Result.Benchmarks)
	}
	if oc.Result.Environment.Calibration.SpawnFloorMs.Start.N == 0 {
		t.Fatal("calibration missing")
	}
	// env_overrides.removed holds variable names, never values (no
	// secret/PATH leakage into results).
	for _, r := range oc.Result.Environment.EnvOverrides.Removed {
		if strings.Contains(r, "=") {
			t.Fatalf("removed holds a value: %q", r)
		}
	}
	if problems := schema.Validate(oc.Result); len(problems) != 0 {
		t.Fatalf("self-produced result invalid: %v", problems)
	}
	// The canonical file must validate from disk too.
	if _, problems, err := schema.ValidateFile(out); err != nil || len(problems) != 0 {
		t.Fatalf("file validate: %v %v", err, problems)
	}
}

// gapBench reports a second metric only on some iterations, declaring
// the rest unavailable.
type gapBench struct {
	mu sync.Mutex
	i  int
}

func (f *gapBench) Spec() bench.Spec {
	return bench.Spec{
		ID: "test.gap", DefinitionVersion: 1, Tier: 1, Kind: bench.KindE2E,
		Metrics: []bench.MetricSpec{
			{Name: "always_ms", Unit: "ms", Direction: bench.LowerIsBetter},
			{Name: "sometimes_ms", Unit: "ms", Direction: bench.LowerIsBetter},
		},
	}
}

func (f *gapBench) Setup(_ context.Context, _ *bench.RunEnv, _ bench.Subject) (bench.Fixture, error) {
	return bench.Fixture{}, nil
}

func (f *gapBench) Iterate(_ context.Context, _ bench.Fixture, _ bench.Subject, it bench.Iter) (bench.Observation, error) {
	if it.Phase != bench.PhaseMeasure {
		return bench.Observation{Values: map[string]float64{"always_ms": 1, "sometimes_ms": 2}, Valid: true}, nil
	}
	f.mu.Lock()
	f.i++
	i := f.i
	f.mu.Unlock()
	obs := bench.Observation{Values: map[string]float64{"always_ms": 10}, Valid: true}
	if i%2 == 0 {
		// Declared gap: absent from Values, with a reason.
		obs.Unavailable = map[string]string{"sometimes_ms": "endpoint not observed"}
	} else {
		obs.Values["sometimes_ms"] = 20
	}
	return obs, nil
}

func (f *gapBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

func TestUnavailableMetricExcludedFromStats(t *testing.T) {
	n, w := 4, 0
	out := filepath.Join(t.TempDir(), "r.json")
	oc, err := Run(context.Background(), Config{
		Benchmarks:     []bench.Benchmark{&gapBench{}},
		Subjects:       testSubjects(t)[:1],
		SubjectProfile: subject.Builtin(),
		Select:         []string{"test.gap"},
		Tier:           -1,
		Profile:        "quick",
		N:              &n,
		Warmup:         &w,
		Seed:           424242,
		Out:            out,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := oc.Result.Benchmarks[0]
	// Every sample stays valid: a declared gap is not a failed run.
	for _, it := range b.Iterations {
		if it.Phase == "measure" && !it.Valid {
			t.Fatalf("unavailable metric must not invalidate the sample: %+v", it)
		}
	}
	byName := map[string]schema.Metric{}
	for _, m := range b.Metrics {
		byName[m.Name] = m
	}
	always := byName["always_ms"]
	if always.Statistics == nil || always.Statistics.ValidN != n {
		t.Fatalf("always_ms stats = %+v, want valid_n %d", always.Statistics, n)
	}
	sometimes := byName["sometimes_ms"]
	if sometimes.Statistics == nil {
		t.Fatal("sometimes_ms must keep statistics from the samples that had it")
	}
	// Zeros must not leak in: only odd iterations (value 20) count.
	if sometimes.Statistics.ValidN != n/2 {
		t.Fatalf("valid_n = %d, want %d (only measured samples)",
			sometimes.Statistics.ValidN, n/2)
	}
	if sometimes.Statistics.Min != 20 || sometimes.Statistics.Max != 20 {
		t.Fatalf("measured samples must be 20: %+v", sometimes.Statistics)
	}
	if reason := sometimes.Statistics.Nulls["missing_samples"]; !strings.Contains(reason, "2 of 4") {
		t.Fatalf("missing_samples reason = %q", reason)
	}
	// The mask is stored on the iteration, so the document alone
	// reproduces the writer's statistics.
	gapped := 0
	for _, it := range b.Iterations {
		if _, ok := it.Attrs[schema.UnavailablePrefix+"sometimes_ms"]; ok {
			gapped++
		}
	}
	if gapped != n/2 {
		t.Fatalf("stored unavailable attrs = %d, want %d", gapped, n/2)
	}
	if _, problems, err := schema.ValidateFile(out); err != nil || len(problems) != 0 {
		t.Fatalf("validate: %v %v", err, problems)
	}
}

func TestTierFilterNoMatch(t *testing.T) {
	out := filepath.Join(t.TempDir(), "r.json")
	_, err := Run(context.Background(), Config{
		Benchmarks: []bench.Benchmark{&recordBench{tier: 1}},
		Tier:       3, // nothing registered at tier 3
		Profile:    "quick",
		Out:        out,
	})
	if err == nil {
		t.Fatal("want no-benchmarks-selected error")
	}
}
