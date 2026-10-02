// Package collect reads process memory from the OS.
//
// It exists because RSS is not one thing: Windows reports a working
// set, Linux reports RSS, macOS reports resident size, and each
// tracks (or does not track) a peak differently. Callers get the value
// together with the semantics tag that says which quantity it is, and
// an explicit reason when no value is available. Nothing here ever
// substitutes zero for a missing measurement, and nothing reports
// virtual memory as resident memory.
//
// Two quantities, never mixed:
//
//   - Current RSS: bytes resident right now (working set on Windows,
//     VmRSS on Linux, resident size on macOS).
//   - Peak RSS: the kernel-tracked high-water mark for the process
//     lifetime where the OS provides one. Where it does not, peak is
//     reported unavailable rather than approximated.
//
// Go runtime heap statistics are deliberately absent: HeapAlloc,
// HeapInuse and Sys are runtime-internal quantities, not OS memory,
// and the plan keeps them as ff-perf marker fields (see internal/
// perfmark) rather than anything this package measures.
package collect

import (
	"fmt"
	"time"
)

// Unavailable reasons. Every one of them is reported as data, never
// as a zero value.
const (
	// ReasonExited: the process was gone before it could be read.
	ReasonExited = "process_exited_before_read"
	// ReasonNoProcess: no process id was supplied.
	ReasonNoProcess = "no_process"
	// ReasonUnsupported: this platform has no equivalent facility.
	ReasonUnsupported = "platform_unsupported"
	// ReasonQueryFailed: the OS query itself failed.
	ReasonQueryFailed = "os_query_failed"
	// ReasonMalformed: the OS answered with data this package could
	// not interpret (unparseable /proc field, zero page count, ...).
	ReasonMalformed = "malformed_os_memory_data"
	// ReasonPermitted: the OS refused access to the process.
	ReasonPermitted = "os_access_denied"
)

// Error carries the reason a measurement is unavailable.
type Error struct {
	Reason string
	Err    error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("collect: %s: %v", e.Reason, e.Err)
	}
	return "collect: " + e.Reason
}

func (e *Error) Unwrap() error { return e.Err }

func unavailable(reason string, err error) error { return &Error{Reason: reason, Err: err} }

// Reading is one process's memory at one instant.
//
// PeakBytes is -1 when the OS does not track a peak for a live
// process; RSSBytes is -1 when the read failed, in which case Reason
// says why.
type Reading struct {
	PID      int
	RSSBytes int64
	// PeakBytes is the kernel-tracked lifetime peak, or -1 when
	// unavailable.
	PeakBytes int64
	// RSSSemantics names the current-RSS quantity, e.g.
	// "windows:working_set" or "linux:vm_rss".
	RSSSemantics string
	// PeakSemantics names the peak quantity, e.g.
	// "windows:peak_working_set". Empty when PeakBytes < 0.
	PeakSemantics string
	// Reason is empty on success.
	Reason string
}

// Ok reports whether a current RSS value was read.
func (r Reading) Ok() bool { return r.RSSBytes >= 0 && r.Reason == "" }

// ReadingOf reads the current and peak memory of one process.
func ReadingOf(pid int) (Reading, error) {
	if pid <= 0 {
		return Reading{PID: pid, RSSBytes: -1, PeakBytes: -1, Reason: ReasonNoProcess},
			unavailable(ReasonNoProcess, nil)
	}
	r, err := readProcess(pid)
	if err != nil {
		r.Reason = reasonOf(err)
		if r.RSSBytes < 0 {
			r.RSSBytes = -1
		}
		if r.PeakBytes > 0 {
			r.PeakBytes = -1
			r.PeakSemantics = ""
		}
		return r, err
	}
	return r, nil
}

func reasonOf(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Reason
	}
	return ReasonQueryFailed
}

// Tree is a process plus its descendants at one instant.
type Tree struct {
	// PIDs holds the root first, then descendants in discovery order.
	PIDs []int
	// RSSBytes is the sum of current RSS over PIDs. -1 when unknown.
	RSSBytes int64
	// Semantics names the summed quantity. Windows and Linux sum
	// different per-process quantities, so the tag says which.
	Semantics string
	// PeakBytes is the kernel-tracked peak of the ROOT process only:
	// no OS exposes a per-tree peak for a live tree on the platforms
	// HPOV supports. Callers wanting a tree peak must sample.
	PeakBytes     int64
	PeakSemantics string
	// Descendants counts PIDs below the root.
	Descendants int
	Reason      string
}

// TreeOf reads the root process and its live descendants.
//
// Summed current RSS is a lower bound on the true instantaneous
// footprint of the tree: a descendant that spawned and exited between
// two reads is never counted. PeakBytes is the root's kernel-tracked
// peak, which is exact for the root alone.
func TreeOf(root int) (Tree, error) {
	if root <= 0 {
		return Tree{RSSBytes: -1, PeakBytes: -1, Reason: ReasonNoProcess},
			unavailable(ReasonNoProcess, nil)
	}
	r, err := ReadingOf(root)
	t := Tree{
		PIDs:          []int{root},
		RSSBytes:      -1,
		PeakBytes:     r.PeakBytes,
		PeakSemantics: r.PeakSemantics,
		Semantics:     r.RSSSemantics,
		Descendants:   0,
	}
	if err != nil {
		t.Reason = r.Reason
		return t, err
	}
	sum := r.RSSBytes
	kids, derr := descendants(root)
	if derr != nil {
		// Descendant enumeration failed: the root reading is still
		// usable, but the caller must not read it as a tree total.
		t.Reason = reasonOf(derr)
		t.Semantics = ""
		t.RSSBytes = sum
		return t, nil
	}
	for _, pid := range kids {
		k, kerr := ReadingOf(pid)
		if kerr != nil || !k.Ok() {
			// A descendant that exited mid-read contributes nothing;
			// that is expected and not a failure.
			continue
		}
		t.PIDs = append(t.PIDs, pid)
		t.Descendants++
		sum += k.RSSBytes
	}
	t.RSSBytes = sum
	return t, nil
}

// DefaultInterval is the process-tree sampling interval.
//
// Chosen from measurement, not taste. The overhead test
// (suites.TestSamplerOverhead) compares the same spawn with and
// without sampling on this class of machine: at 5 ms the workload grew
// ~6.8%, at 10 ms ~1.4%, with the sampled peak stable in both cases
// because the ff workload's resident set rises once during startup and
// then holds. 10 ms therefore keeps the tree figure useful at a cost
// the workload cannot feel, which is the right side of the tradeoff:
// more samples would catch a shorter-lived spike, fewer would risk
// missing one entirely.
//
// A sampled peak remains a lower bound whatever the interval; the
// interval is recorded with every reported value so a reader can judge
// the resolution.
const DefaultInterval = 10 * time.Millisecond

// Sampler polls a process tree and keeps the peak sum it observed.
// A sampled peak is a lower bound: a spike entirely between two
// samples is invisible. Callers must label such values as sampled.
type Sampler struct {
	interval time.Duration
	// intervalSet records the effective interval for reporting.
	intervalSet bool

	samples    int
	peakBytes  int64
	peakAt     time.Time
	descMax    int
	semantics  string
	failures   int
	lastReason string
}

// NewSampler returns a Sampler using DefaultInterval.
func NewSampler() *Sampler {
	return &Sampler{interval: DefaultInterval, peakBytes: -1}
}

// NewSamplerInterval returns a Sampler with an explicit interval.
func NewSamplerInterval(d time.Duration) *Sampler {
	if d <= 0 {
		d = DefaultInterval
	}
	s := NewSampler()
	s.interval = d
	s.intervalSet = true
	return s
}

// Interval reports the effective sampling interval.
func (s *Sampler) Interval() time.Duration { return s.interval }

// Sample takes one tree reading and folds it into the peak. It returns
// the current tree RSS in bytes, or -1 when unreadable.
func (s *Sampler) Sample(root int) int64 {
	t, err := TreeOf(root)
	if err != nil && t.RSSBytes < 0 {
		s.failures++
		s.lastReason = t.Reason
		return -1
	}
	s.samples++
	if t.Semantics != "" {
		s.semantics = t.Semantics
	}
	if t.Descendants > s.descMax {
		s.descMax = t.Descendants
	}
	if t.RSSBytes > s.peakBytes {
		s.peakBytes = t.RSSBytes
		s.peakAt = time.Now()
	}
	return t.RSSBytes
}

// Report summarizes the sampling run. A Sampler with no successful
// sample reports Reason from the last failure and PeakBytes -1.
type Report struct {
	// PeakBytes is the largest summed tree RSS observed, or -1.
	PeakBytes int64
	// Sampled is true: the value is a sampled peak (a lower bound),
	// never an exact kernel-tracked one.
	Sampled    bool
	Semantics  string
	Interval   time.Duration
	Samples    int
	DescMax    int
	Failures   int
	Reason     string
	PeakOffset time.Duration // when the peak was seen
}

// Report summarizes the sampler for storage as benchmark metadata.
func (s *Sampler) Report() Report {
	return Report{
		PeakBytes:  s.peakBytes,
		Sampled:    true,
		Semantics:  s.semantics,
		Interval:   s.interval,
		Samples:    s.samples,
		DescMax:    s.descMax,
		Failures:   s.failures,
		Reason:     s.lastReason,
		PeakOffset: time.Since(s.peakAt),
	}
}
