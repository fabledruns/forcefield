package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"forcefield/internal/hpov/stats"
)

// ValidateFile parses path (unknown fields are ignored, per the
// forward-compatibility rule) and checks structural integrity plus
// recomputed statistics. It returns the parsed result and a list of
// problems; an empty list means valid.
func ValidateFile(path string) (*Result, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read result: %w", err)
	}
	var r Result
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	// No DisallowUnknownFields: readers must ignore unknown fields.
	if err := dec.Decode(&r); err != nil {
		return nil, nil, fmt.Errorf("parse result: %w", err)
	}
	return &r, Validate(&r), nil
}

// Validate recomputes statistics from raw samples and checks the
// document hangs together. Compare always recomputes from raw
// values, ignoring stored statistics; this is the same recomputation.
func Validate(r *Result) []string {
	var problems []string
	add := func(f string, args ...any) {
		problems = append(problems, fmt.Sprintf(f, args...))
	}
	if r.Schema != SchemaName {
		add("schema: got %q, want %q", r.Schema, SchemaName)
	}
	major := strings.SplitN(r.SchemaVersion, ".", 2)[0]
	if want := strings.SplitN(SchemaVersion, ".", 2)[0]; major != want {
		add("schema_version major: got %q, want major %q", r.SchemaVersion, want)
	}
	if r.Run.QuantileMethod != QuantileMethod {
		add("run.quantile_method: got %q, want %q", r.Run.QuantileMethod, QuantileMethod)
	}
	if r.Suite.Profile == "" {
		add("suite.profile: empty")
	}
	for _, b := range r.Benchmarks {
		switch b.Status {
		case StatusOK, StatusSkipped, StatusUnsupported, StatusInvalid, StatusError:
		default:
			add("%s: unknown status %q", b.ID, b.Status)
			continue
		}
		if b.Status != StatusOK {
			continue
		}
		measure := 0
		for _, it := range b.Iterations {
			if it.Phase == "measure" {
				measure++
			}
		}
		for _, m := range b.Metrics {
			if len(m.Values) != measure {
				add("%s/%s: %d values but %d measure iterations",
					b.ID, m.Name, len(m.Values), measure)
				continue
			}
			if m.Statistics == nil {
				if validCount(b.Iterations) > 0 {
					add("%s/%s: missing statistics", b.ID, m.Name)
				}
				continue
			}
			valid := make([]bool, 0, measure)
			for _, it := range b.Iterations {
				if it.Phase != "measure" {
					continue
				}
				// A declared gap excludes the sample from this metric
				// without invalidating the iteration, matching how the
				// writer summarized it.
				if _, na := it.Attrs[UnavailablePrefix+m.Name]; na {
					valid = append(valid, false)
					continue
				}
				valid = append(valid, it.Valid)
			}
			got := stats.Summarize(m.Values, valid, r.Run.Seed)
			checkSummary(b.ID, m.Name, m.Statistics, got, &problems)
		}
	}
	return problems
}

// validCount counts valid measure-phase iterations.
func validCount(iters []Iteration) int {
	n := 0
	for _, it := range iters {
		if it.Phase == "measure" && it.Valid {
			n++
		}
	}
	return n
}

func checkSummary(id, metric string, want *Statistics, got *stats.Summary, problems *[]string) {
	add := func(f string, args ...any) {
		*problems = append(*problems, fmt.Sprintf(id+"/"+metric+": "+f, args...))
	}
	if got == nil {
		if want.ValidN != 0 || want.N != 0 {
			add("statistics present but no samples")
		}
		return
	}
	const tol = 1e-9
	eq := func(a, b float64) bool {
		if math.IsNaN(a) && math.IsNaN(b) {
			return true
		}
		return math.Abs(a-b) <= tol*(1+math.Abs(a)+math.Abs(b))
	}
	if want.N != got.N || want.ValidN != got.ValidN {
		add("n/valid_n: got %d/%d, recomputed %d/%d", want.N, want.ValidN, got.N, got.ValidN)
	}
	if got.ValidN == 0 {
		return
	}
	pairs := map[string][2]float64{
		"min": {want.Min, got.Min}, "p50": {want.P50, got.P50},
		"max": {want.Max, got.Max}, "mean": {want.Mean, got.Mean},
		"stdev": {want.Stdev, got.Stdev}, "mad": {want.MAD, got.MAD},
		"iqr": {want.IQR, got.IQR},
	}
	for name, wg := range pairs {
		if !eq(wg[0], wg[1]) {
			add("%s: stored %v, recomputed %v", name, wg[0], wg[1])
		}
	}
	pct := func(name string, w *float64, g *float64) {
		if (w == nil) != (g == nil) {
			add("%s: null mismatch (stored null=%v, recomputed null=%v)", name, w == nil, g == nil)
			return
		}
		if w != nil && !eq(*w, *g) {
			add("%s: stored %v, recomputed %v", name, *w, *g)
		}
	}
	pct("p90", want.P90, got.P90)
	pct("p95", want.P95, got.P95)
	pct("p99", want.P99, got.P99)
	if !eq(want.CI95P50[0], got.CI95P50[0]) || !eq(want.CI95P50[1], got.CI95P50[1]) {
		add("ci95_p50: stored %v, recomputed %v", want.CI95P50, got.CI95P50)
	}
}
