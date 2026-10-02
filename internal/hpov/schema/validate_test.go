package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/hpov/stats"
)

func testResult() *Result {
	vals := make([]float64, 30)
	for i := range vals {
		vals[i] = 120 + float64(i%7)
	}
	valid := make([]bool, 30)
	for i := range valid {
		valid[i] = true
	}
	sm := stats.Summarize(vals, valid, 424242)
	st := &Statistics{
		N: sm.N, ValidN: sm.ValidN, Min: sm.Min, P50: sm.P50,
		P90: sm.P90, P95: sm.P95, P99: sm.P99, Max: sm.Max,
		Mean: sm.Mean, Stdev: sm.Stdev, MAD: sm.MAD, IQR: sm.IQR,
		RobustCV: sm.RobustCV, CI95P50: sm.CI95P50, Nulls: sm.Nulls,
	}
	iters := make([]Iteration, 0, 35)
	for i := 0; i < 5; i++ {
		iters = append(iters, Iteration{I: i, Phase: "warmup", Valid: true, TOffsetMs: float64(i)})
	}
	for i, v := range vals {
		iters = append(iters, Iteration{I: 5 + i, Phase: "measure", Valid: true,
			TOffsetMs: float64(100 + i), Values: map[string]float64{"wall_ms": v}})
	}
	return &Result{
		Schema: SchemaName, SchemaVersion: SchemaVersion,
		Suite: Suite{Name: "hpov", Version: SuiteVersion, Profile: "standard", DefinitionSet: DefinitionSet},
		Run: Run{ID: "t", StartedAt: "2026-10-02T00:00:00Z", FinishedAt: "2026-10-02T00:01:00Z",
			Seed: 424242, QuantileMethod: QuantileMethod,
			Bootstrap: Bootstrap{Resamples: BootstrapResamples, Seed: 424242, Method: "percentile"},
			Quality:   RunQuality{Label: "good"}},
		Host:        Host{HostID: "h:x", OS: "windows", Arch: "amd64", Env: "native", Gomaxprocs: 8, HPOVGoVersion: "go1.26.4"},
		Environment: Environment{HomeIsolated: true, Stdin: "null", MarkersWallPass: "off"},
		Benchmarks: []Benchmark{{
			ID: "launch.version", DefinitionVersion: 1, Tier: 1, Kind: "e2e",
			Status: StatusOK, Iterations: iters,
			Metrics: []Metric{{Name: "wall_ms", Unit: "ms", Direction: "lower_is_better",
				Values: vals, Statistics: st}},
		}},
	}
}

func writeTmp(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateRoundTrip(t *testing.T) {
	p := writeTmp(t, testResult())
	_, problems, err := ValidateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
}

func TestValidateDetectsMismatch(t *testing.T) {
	r := testResult()
	p50 := r.Benchmarks[0].Metrics[0].Statistics.P50 + 50
	r.Benchmarks[0].Metrics[0].Statistics.P50 = p50
	p := writeTmp(t, r)
	_, problems, err := ValidateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("tampered p50 must be detected")
	}
}

func TestValidateToleratesUnknownFields(t *testing.T) {
	raw, _ := json.Marshal(testResult())
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["zzz_future_additive_field"] = 1
	p := writeTmp(t, m)
	_, problems, err := ValidateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("unknown fields must be ignored: %v", problems)
	}
}

func TestValidateCountsValues(t *testing.T) {
	r := testResult()
	r.Benchmarks[0].Metrics[0].Values = r.Benchmarks[0].Metrics[0].Values[:10]
	p := writeTmp(t, r)
	_, problems, err := ValidateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 {
		t.Fatal("values/iterations count mismatch must be detected")
	}
}
