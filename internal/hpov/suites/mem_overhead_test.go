package suites

import (
	"context"
	"os"
	"testing"
	"time"

	"forcefield/internal/hpov/collect"
	"forcefield/internal/hpov/spawn"
)

// TestSamplerOverhead measures what polling costs the workload, so the
// choice of DefaultInterval is justified by data rather than taste.
//
// The comparison is the same spawn with and without the sampling hook.
// It is a diagnostic, not a gate: the numbers are logged and the test
// only fails if sampling changes the child's own wall time beyond a
// wide margin (i.e. the harness itself became the bottleneck).
func TestSamplerOverhead(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HPOV_SUITES_FAKE=1", "HPOV_FAKE_EXIT=1"}
	run := func(sample bool) time.Duration {
		opts := spawn.Options{
			Path: self, Args: []string{"run", "--agent", "__bench_bogus__", "x"},
			Env: env, Timeout: 30 * time.Second,
		}
		if sample {
			opts.SampleInterval = collect.DefaultInterval
			// The real tree query, so the number reflects the actual
			// cost of measuring rather than of an empty callback.
			s := collect.NewSampler()
			opts.Sample = func(pid int) { s.Sample(pid) }
		}
		res, err := spawn.Run(context.Background(), opts)
		if err != nil {
			t.Fatalf("spawn: %v", err)
		}
		return time.Duration(res.WallMS * float64(time.Millisecond))
	}
	// Warm up both paths, then alternate to spread machine drift.
	run(false)
	run(true)
	var plain, sampled time.Duration
	const rounds = 8
	for i := 0; i < rounds; i++ {
		plain += run(false)
		sampled += run(true)
	}
	p := plain / rounds
	s := sampled / rounds
	d := s - p
	t.Logf("spawn wall: plain=%v sampled=%v delta=%v (%.1f%%) at interval=%v",
		p, s, d, 100*float64(d)/float64(p), collect.DefaultInterval)

	// A wide guard: the point is to catch an accidental busy-loop, not
	// to police microseconds of scheduler noise.
	if d > 200*time.Millisecond {
		t.Fatalf("sampling inflated the workload by %v: the harness is distorting it", d)
	}
}
