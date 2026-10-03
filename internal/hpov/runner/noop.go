package runner

import (
	"context"
	"os"
	"sort"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/fixture"
	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/spawn"
	"forcefield/internal/hpov/stats"
)

// calibrationN is the fixed spawn-floor sample count at each end of a
// run. It quantifies runner overhead and drift and is never
// subtracted from results.
const calibrationN = 30

// NoopBenchmark measures the harness floor: re-exec of the hpov
// binary into the trivial __noop mode with the same spawn code every
// benchmark uses. Tier 0 (infrastructure): it runs implicitly for
// calibration and explicitly when selected.
func NoopBenchmark(self string) bench.Benchmark {
	return &noopBench{self: self}
}

type noopBench struct{ self string }

func (n *noopBench) NeedsSubject() bool { return false }

func (n *noopBench) Spec() bench.Spec {
	return bench.Spec{
		ID:                "calibration.noop",
		DefinitionVersion: 1,
		Title:             "Spawn-floor calibration",
		Purpose:           "Trivial hpov __noop re-exec through the same spawn path; quantifies runner overhead and drift.",
		Kind:              bench.KindE2E,
		Tier:              0,
		Metrics: []bench.MetricSpec{
			{Name: "wall_ms", Unit: "ms", Direction: bench.LowerIsBetter},
			{Name: "cpu_ms", Unit: "ms", Direction: bench.LowerIsBetter},
		},
		Params:    map[string]string{"workload": "hpov __noop"},
		Predicate: "exit_code==0",
	}
}

func (n *noopBench) Setup(_ context.Context, _ *bench.RunEnv, _ bench.Subject) (bench.Fixture, error) {
	return bench.Fixture{}, nil
}

func (n *noopBench) Iterate(ctx context.Context, _ bench.Fixture, _ bench.Subject, _ bench.Iter) (bench.Observation, error) {
	env, _ := fixture.ScrubEnv(nil, bench.Env{})
	self := n.self
	if self == "" {
		var err error
		self, err = os.Executable()
		if err != nil {
			return bench.Observation{}, err
		}
	}
	res, err := spawn.Run(ctx, spawn.Options{
		Path: self,
		Args: []string{"__noop"},
		Env:  env,
	})
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	obs := bench.Observation{
		Values: map[string]float64{
			"wall_ms": res.WallMS,
			"cpu_ms":  res.UserMS + res.SysMS,
		},
		Valid: res.ExitCode == 0 && !res.TimedOut,
		Attrs: map[string]string{"exit_code": itoa(res.ExitCode)},
	}
	if !obs.Valid {
		obs.InvalidReason = "exit_code!=0 or timeout"
	}
	return obs, nil
}

func (n *noopBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

// calibrate runs the noop workload calibrationN times and summarizes
// the wall distribution.
func calibrate(ctx context.Context, seed int64) (schema.CalibrationPoint, []string) {
	self, err := os.Executable()
	if err != nil {
		return schema.CalibrationPoint{}, []string{"calibration unavailable: " + err.Error()}
	}
	nb := &noopBench{self: self}
	var walls []float64
	for i := 0; i < calibrationN; i++ {
		obs, err := nb.Iterate(ctx, bench.Fixture{}, bench.Subject{}, bench.Iter{Phase: bench.PhaseMeasure})
		if err != nil || !obs.Valid {
			continue
		}
		walls = append(walls, obs.Values["wall_ms"])
	}
	if len(walls) == 0 {
		return schema.CalibrationPoint{}, []string{"calibration failed: no valid noop samples"}
	}
	sort.Float64s(walls)
	_ = seed
	p50 := stats.NearestRank(walls, 50)
	p95 := stats.NearestRank(walls, 95)
	return schema.CalibrationPoint{P50: p50, P95: &p95, N: len(walls)}, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [8]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
