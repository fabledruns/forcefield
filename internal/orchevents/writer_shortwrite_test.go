package orchevents

// ORCH-TEST-04: deterministic fault injection at the writer's *os.File
// seam. The writer's f is a concrete *os.File (not an io.Writer), so an
// ENOSPC-style mid-record failure cannot be shimmed in; instead these
// tests drive failures the OS actually produces on demand — a broken
// pipe with the reader gone, and a hand-reconstructed truncated tail in
// the exact on-disk shape a short write leaves — with no sleeps and no
// dependence on the local filesystem ever running out of space.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriterMidRecordShortWriteLatch pins the mid-stream failure
// contract: a write that was accepted (record 1), followed by an
// OS-level write failure (record 2), must latch the first failure into
// Err(), count the failed record as written-but-not-really (no byte or
// bookkeeping success), keep earlier failures over later ones, and let
// Close() stay silent about a healthy fd while the latch survives.
func TestWriterMidRecordShortWriteLatch(t *testing.T) {
	rEnd, wEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	w := NewFileWriter(wEnd, "run-enospc")

	// Write 1 is accepted into the pipe buffer while the reader is
	// still open — the log existed and worked before the fault.
	w.Emit(Event{Type: RunStarted})
	if w.Err() != "" {
		t.Fatalf("first emit latched %q, want a clean write", w.Err())
	}
	accepted := w.bytes
	if accepted == 0 {
		t.Fatalf("bytes = %d after an accepted record, want its length", w.bytes)
	}

	// Deterministic ENOSPC stand-in: the reader goes away mid-stream.
	// Every later write fails at the OS level, immediately, on every
	// platform — a disk that filled up after the log was opened.
	if err := rEnd.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}

	// Write 2 fails as a whole-record write: the record was never
	// accepted, and the failure is latched exactly once.
	w.Emit(Event{Type: RunCompleted, Outcome: OutcomeDone})
	if w.Err() == "" {
		t.Fatal("a failed event write left Err() empty: a mid-record write failure must never be silently treated as success")
	}
	if !strings.HasPrefix(w.Err(), "write event 2: ") {
		t.Errorf("latched error = %q, want the write event 2: wrap naming the failed record", w.Err())
	}
	if !strings.Contains(w.Err(), "pipe") {
		t.Errorf("latched error = %q, want the underlying broken-pipe failure named", w.Err())
	}
	if w.bytes != accepted {
		t.Errorf("bytes = %d after the failed emit, want %d: failed records must not be counted as written", w.bytes, accepted)
	}
	if w.capped {
		t.Error("capped = true, want false: a mid-stream OS write failure is not the 4 MiB truncation path")
	}

	// The contract is failure-latching, not failure-fail-fast: later
	// emissions still try (and fail), but must never replace the first
	// recorded error — that one is the diagnosis.
	firstErr := w.Err()
	w.Emit(Event{Type: RunFailed, Outcome: OutcomeError})
	if w.Err() != firstErr {
		t.Errorf("Err() after a second failed emit = %q, want the retained first failure %q", w.Err(), firstErr)
	}

	// Close: the write fd is still healthy, so Close reports nil — the
	// write failure lives in Err(), and Sticky stays true after Close.
	if err := w.Close(); err != nil {
		t.Errorf("Close = %v, want nil: a latched write failure must not masquerade as a close failure", err)
	}
	if w.Err() != firstErr {
		t.Errorf("Err() after Close = %q, want the retained first failure %q", w.Err(), firstErr)
	}
}

// TestWriterMidRecordShortWriteNoSuccessOnPartialTail pins reader
// recovery against the exact on-disk shape a mid-record ENOSPC leaves:
// complete earlier records stay readable, the truncated final record is
// never presented as a complete event, and the error is positioned at
// the malformed tail.
func TestWriterMidRecordShortWriteNoSuccessOnPartialTail(t *testing.T) {
	partialTail := `{"seq":2,"ts":"2026-11-04T10:00:01Z","run":"run-enospc","type":"run_completed","status":"ver`

	t.Run("truncated tail keeps the readable prefix and reports line 2", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sess-partial.events.jsonl")

		// Record 1 through the real writer, so the prefix on disk is
		// the writer's true wire shape, not a hand-rolled copy.
		w, err := NewWriter(path, "run-enospc")
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		w.Emit(Event{Type: RunStarted})
		if w.Err() != "" {
			t.Fatalf("first emit latched %q, want a clean write", w.Err())
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		// Reconstruct the on-disk end state of a short write: record 2
		// stops mid-JSON — no closing brace, no trailing newline.
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("append to event log: %v", err)
		}
		if _, err := f.Write([]byte(partialTail)); err != nil {
			f.Close()
			t.Fatalf("write truncated tail: %v", err)
		}
		f.Close()

		events, err := ReadAll(path)
		if err == nil {
			t.Fatalf("ReadAll = nil error on %d events: a truncated JSON tail must never parse as clean success", len(events))
		}
		if len(events) != 1 {
			t.Fatalf("recovered %d events, want exactly the 1 complete record before the truncated tail", len(events))
		}
		if events[0].Seq != 1 || events[0].Type != RunStarted || events[0].RunID != "run-enospc" {
			t.Errorf("recovered prefix = %+v, want the complete run_started record", events[0])
		}
		if !strings.Contains(err.Error(), "parse event line 2") {
			t.Errorf("error = %q, want the positioned parse failure naming the malformed tail line", err.Error())
		}
	})

	t.Run("trailing empty line after the last newline stays clean evidence", func(t *testing.T) {
		// The honest-interruption boundary: a crash between writes
		// leaves an empty final line, which is tolerated, unlike a
		// truncated record's partial JSON.
		dir := t.TempDir()
		path := filepath.Join(dir, "sess-blanktail.events.jsonl")

		w, err := NewWriter(path, "run-enospc")
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		w.Emit(Event{Type: RunStarted})
		if w.Err() != "" {
			t.Fatalf("first emit latched %q, want a clean write", w.Err())
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("append to event log: %v", err)
		}
		if _, err := f.WriteString("\n"); err != nil {
			f.Close()
			t.Fatalf("write trailing newline: %v", err)
		}
		f.Close()

		events, err := ReadAll(path)
		if err != nil {
			t.Fatalf("ReadAll = %v, want a tolerated trailing blank line", err)
		}
		if len(events) != 1 || events[0].Seq != 1 || events[0].Type != RunStarted {
			t.Fatalf("events = %+v, want exactly record 1", events)
		}
	})
}
