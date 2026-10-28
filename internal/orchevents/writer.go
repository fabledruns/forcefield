package orchevents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxFileBytes bounds one run's event file; past it the writer records
// a single truncation marker and drops the rest. The bound matches the
// trace file cap: evidence must never grow with run duration without
// bound, and heavy multi-child delegations stay inside one bounded file.
const maxFileBytes = 4 << 20

// Writer is an append-only JSONL handle for one headless run's event
// file. It is safe for concurrent Emit: the runtime emits delegation
// events from scheduler goroutines while the CLI driver records the
// main run's lifecycle from its consumer loop.
//
// A writer is observability, never load-bearing: every write failure
// latches into Err() and future emissions no-op; the run carrying the
// writer decides what the lost evidence means for its exit code.
type Writer struct {
	mu     sync.Mutex
	f      *os.File
	runID  string
	seq    int64
	bytes  int64
	err    string
	capped bool
	closed bool
}

// NewWriter opens (creating parent directories and the file as needed)
// the event log at path. runID, when empty, is generated here — one
// fresh UUID for the whole file — so every event produced inside one
// process run correlates without producers threading a run id through
// each call.
func NewWriter(path, runID string) (*Writer, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create event log directory: %w", err)
		}
		_ = os.Chmod(dir, 0o700)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	if runID == "" {
		runID = RunUUID()
	}
	return &Writer{f: f, runID: runID}, nil
}

// Err returns the first write error observed, or "". Sticky: a later
// successful write does not clear it — evidence durability once lost is
// not retroactively restored.
func (w *Writer) Err() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// RunID returns the run id stamped on this writer's events.
func (w *Writer) RunID() string {
	if w == nil {
		return ""
	}
	return w.runID
}

// Close releases the file. Idempotent and nil-safe.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Emit records one event. Seq, Ts, and RunID (when left empty) are the
// writer's job; free-text fields are scrubbed defensively. Events that
// violate the schema (unknown type, child events without attribution)
// are dropped silently: a malformed event is a producer bug, and
// recording it would poison the stream's schema guarantees.
func (w *Writer) Emit(e Event) {
	if w == nil {
		return
	}
	if !e.valid() {
		return
	}
	sanitize(&e)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.capped || w.f == nil {
		return
	}
	w.seq++
	e.Seq = w.seq
	e.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	if e.RunID == "" {
		// One process run is one writer: the run id is the writer's,
		// and child runs correlate through ChildRunID + Chain.
		e.RunID = w.runID
	}
	raw, err := json.Marshal(e)
	if err != nil {
		w.latchLocked(fmt.Errorf("encode event %d: %w", e.Seq, err))
		return
	}
	raw = append(raw, '\n')
	if _, err := w.f.Write(raw); err != nil {
		w.latchLocked(fmt.Errorf("write event %d: %w", e.Seq, err))
		return
	}
	w.bytes += int64(len(raw))
	if w.bytes >= maxFileBytes {
		w.capped = true
		end, _ := json.Marshal(Event{Seq: w.seq + 1, Ts: time.Now().UTC().Format(time.RFC3339Nano), RunID: w.runID, Type: "events_truncated"})
		end = append(end, '\n')
		_, _ = w.f.Write(end)
	}
}

func (w *Writer) latchLocked(err error) {
	if w.err == "" {
		w.err = err.Error()
	}
}
