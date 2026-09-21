// Package perfmark emits env-gated startup markers for the performance
// benchmark lab. When FF_PERF_MARKERS is unset (normal use) every probe
// is a single boolean check with no output and no measurable overhead.
// When set, probes write greppable `ff-perf <event>` lines to stderr
// (never stdout, which belongs to the TUI renderer) for an external
// benchmark driver to timestamp. Nothing here affects program behavior:
// no control flow depends on these calls.
package perfmark

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
)

// enabled snapshots FF_PERF_MARKERS at init so disabled runs pay one
// branch per probe and nothing else.
var enabled = os.Getenv("FF_PERF_MARKERS") != ""

// output is stderr in production and swapped in tests. A mutex guards
// it because tests swap concrete types (atomic.Value forbids that).
var (
	outputMu sync.RWMutex
	output   io.Writer = os.Stderr
)

func init() {
	// Snapshot assignment (kept explicit so tests see the seam).
	outputMu.Lock()
	output = io.Writer(os.Stderr)
	outputMu.Unlock()
}

// Enabled reports whether startup markers are active.
func Enabled() bool {
	return enabled
}

// swapOutput redirects marker output for tests, returning a restore func.
func swapOutput(w io.Writer) func() {
	outputMu.Lock()
	defer outputMu.Unlock()
	prev := output
	output = w
	return func() {
		outputMu.Lock()
		defer outputMu.Unlock()
		output = prev
	}
}

// Format renders one marker line. Zero alloc/sys omits memory fields.
func Format(event string, alloc, sys uint64) string {
	if alloc == 0 && sys == 0 {
		return "ff-perf " + event + "\n"
	}
	return fmt.Sprintf("ff-perf %s alloc=%d sys=%d\n", event, alloc, sys)
}

// emit writes one line when enabled.
func emit(line string) {
	if !enabled {
		return
	}
	outputMu.RLock()
	defer outputMu.RUnlock()
	_, _ = io.WriteString(output, line)
}

// Event records a named startup point.
func Event(name string) {
	if !enabled {
		return
	}
	emit(Format(name, 0, 0))
}

// EventMem records a named startup point with current Go heap stats.
// The stop-the-world pause is sub-millisecond on startup heaps and is
// documented in the benchmark methodology.
func EventMem(name string) {
	if !enabled {
		return
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	emit(Format(name, mem.HeapAlloc, mem.Sys))
}
