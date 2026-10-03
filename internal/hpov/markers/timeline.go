package markers

import (
	"strconv"
	"strings"
	"time"
)

// StampedLine is one observed marker occurrence with its
// reader-receipt timestamp. Markers carry no in-process timestamps
// (M3), so the reader's clock is the timing source for every phase
// delta.
type StampedLine struct {
	At   time.Time
	Text string
}

// MemMark is the Go-heap diagnostic attached to EventMem marks.
// HeapAlloc is never presented as memory usage.
type MemMark struct {
	Alloc uint64
	Sys   uint64
}

// Timeline holds first-occurrence marks in emission order.
type Timeline struct {
	Marks map[string]time.Time
	Order []string
	Mem   map[string]MemMark
}

// Build extracts the marker timeline from stamped lines. Markers reach
// the driver on the child's own stderr pipe, one per line and in
// emission order; the first occurrence of each event wins and
// malformed lines are skipped.
func (p Protocol) Build(lines []StampedLine) Timeline {
	tl := Timeline{Marks: map[string]time.Time{}, Mem: map[string]MemMark{}}
	for _, l := range lines {
		ev, ok := p.Event(l.Text)
		if !ok {
			continue
		}
		if _, seen := tl.Marks[ev]; seen {
			continue
		}
		tl.Marks[ev] = l.At
		tl.Order = append(tl.Order, ev)
		tl.Mem[ev] = memMark(l.Text, p.Prefix)
	}
	return tl
}

// memMark reads the EventMem alloc=/sys= fields from a marker line.
// These describe the subject's language runtime and exist only for
// subjects whose instrumentation reports them.
func memMark(line, prefix string) MemMark {
	_, body, ok := strings.Cut(line, prefix)
	if !ok {
		return MemMark{}
	}
	fields := strings.Fields(body)
	var mm MemMark
	// fields[0] is the event name; alloc/sys follow it.
	for _, f := range fields[min(1, len(fields)):] {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "alloc":
			mm.Alloc = n
		case "sys":
			mm.Sys = n
		}
	}
	return mm
}

// Ms returns milliseconds from `since` to the mark. Segments are
// adjacent marks on a single run (telescoping): callers only report a
// delta when both endpoints exist.
func (t Timeline) Ms(event string, since time.Time) (float64, bool) {
	at, ok := t.Marks[event]
	if !ok {
		return 0, false
	}
	return float64(at.Sub(since).Nanoseconds()) / 1e6, true
}

// Missing returns the required events with no mark.
func (t Timeline) Missing(required []string) []string {
	var out []string
	for _, r := range required {
		if _, ok := t.Marks[r]; !ok {
			out = append(out, r)
		}
	}
	return out
}
