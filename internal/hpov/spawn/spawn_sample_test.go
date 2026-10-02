package spawn

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSamplingRunsAcrossChildLifetime verifies the sampling hook fires
// while the child is alive and stops once Run returns, so a sampler can
// never keep reading a pid the OS has recycled.
func TestSamplingRunsAcrossChildLifetime(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var pids sync.Map
	res, err := Run(context.Background(), Options{
		Path: self, Args: []string{"-test.run=TestSamplingChild"},
		Env:            []string{"PATH=" + os.Getenv("PATH"), "HPOV_SPAWN_CHILD=1"},
		SampleInterval: 2 * time.Millisecond,
		Sample: func(pid int) {
			calls.Add(1)
			pids.Store(pid, true)
		},
		Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("child exit = %d", res.ExitCode)
	}
	if calls.Load() < 2 {
		t.Fatalf("sampler ran %d times over a ~100ms child: hook is not wired to the lifetime",
			calls.Load())
	}
	seen := map[int]bool{}
	pids.Range(func(k, _ any) bool {
		seen[k.(int)] = true
		return true
	})
	if len(seen) != 1 {
		t.Fatalf("sampler saw %d pids, want exactly the child's", len(seen))
	}

	// After Run returns the hook must be silent.
	before := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != before {
		t.Fatalf("sampler kept running after Run returned: %d -> %d", before, calls.Load())
	}
}

// TestNoSamplingWhenIntervalZero keeps the default path free: a benchmark
// that only needs the kernel-tracked peak must not spawn a sampler.
func TestNoSamplingWhenIntervalZero(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	_, err = Run(context.Background(), Options{
		Path: self, Args: []string{"-test.run=TestSamplingChild"},
		Env: []string{"PATH=" + os.Getenv("PATH"), "HPOV_SPAWN_CHILD=1"},
		Sample: func(int) {
			calls.Add(1)
		},
		Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("sampler ran %d times with no interval", calls.Load())
	}
}

func TestSamplingChild(t *testing.T) {
	if os.Getenv("HPOV_SPAWN_CHILD") == "" {
		return
	}
	time.Sleep(100 * time.Millisecond)
}
