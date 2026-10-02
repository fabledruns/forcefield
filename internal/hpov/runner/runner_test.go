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

func (f *recordBench) Setup(_ context.Context, _ *bench.RunEnv) (bench.Fixture, error) {
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
	// Probe only stats+hashes; --version failure is tolerated.
	return []bench.Subject{{Label: "base", Path: self}, {Label: "head", Path: self}}
}

func runFake(t *testing.T, seed int64) ([]schema.Benchmark, []string) {
	t.Helper()
	fb := &recordBench{tier: 1}
	n, w := 4, 1
	out := filepath.Join(t.TempDir(), "r.json")
	oc, err := Run(context.Background(), Config{
		Benchmarks: []bench.Benchmark{fb},
		Subjects:   testSubjects(t),
		Select:     []string{"test.fake"},
		Tier:       -1,
		Profile:    "quick",
		N:          &n,
		Warmup:     &w,
		Seed:       seed,
		Out:        out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(oc.Result.Benchmarks) != 2 {
		t.Fatalf("want 2 entries (one per subject), got %d", len(oc.Result.Benchmarks))
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
