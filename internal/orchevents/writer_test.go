package orchevents

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustWriter(t *testing.T, dir string) (*Writer, string) {
	t.Helper()
	path := filepath.Join(dir, "sess-1.events.jsonl")
	w, err := NewWriter(path, "run-abc")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if w.RunID() != "run-abc" {
		t.Errorf("RunID = %q, want run-abc", w.RunID())
	}
	return w, path
}

func TestWriterStampsSeqTsRunID(t *testing.T) {
	dir := t.TempDir()
	w, path := mustWriter(t, dir)
	defer w.Close()

	w.Emit(Event{Type: RunStarted})
	w.Emit(Event{Type: RunCompleted, Outcome: OutcomeDone, Status: "verified"})

	events, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Errorf("event %d seq = %d, want %d", i, e.Seq, i+1)
		}
		if e.RunID != "run-abc" {
			t.Errorf("event %d run = %q, want run-abc", i, e.RunID)
		}
		if _, err := time.Parse(time.RFC3339Nano, e.Ts); err != nil {
			t.Errorf("event %d ts %q unparseable: %v", i, e.Ts, err)
		}
	}
	if events[1].Outcome != OutcomeDone || events[1].Status != "verified" {
		t.Errorf("completed event = %+v, want outcome done + status verified", events[1])
	}
}

func TestWriterCloseIdempotentAndNilSafe(t *testing.T) {
	var nilW *Writer
	nilW.Close() // no panic
	nilW.Emit(Event{Type: RunStarted})
	if nilW.Err() != "" || nilW.RunID() != "" {
		t.Errorf("nil writer accessors misbehaved")
	}
	dir := t.TempDir()
	w, _ := mustWriter(t, dir)
	if err := w.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
	// Emit after close must not panic and must not write.
	w.Emit(Event{Type: RunStarted})
}

func TestWriterConcurrentEmitGapsFreeSeq(t *testing.T) {
	dir := t.TempDir()
	w, path := mustWriter(t, dir)
	defer w.Close()

	const producers, perProducer = 8, 25
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				w.Emit(Event{
					Type:       ChildProgress,
					ChildRunID: "child-" + string(rune('a'+p)),
					Chain:      "main > devops",
					Turns:      i + 1,
				})
			}
		}(p)
	}
	wg.Wait()

	events, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != producers*perProducer {
		t.Fatalf("got %d events, want %d", len(events), producers*perProducer)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("seq not gap-free at %d: got %d", i, e.Seq)
		}
	}
}

func TestWriterScrubAndSchemaGate(t *testing.T) {
	dir := t.TempDir()
	w, path := mustWriter(t, dir)
	defer w.Close()

	// Secret-shaped error text is scrubbed before it reaches disk.
	w.Emit(Event{
		Type:   RunFailed,
		Chain:  "main",
		Error:  "boom with APIKEY=sk-proj-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890 inside",
		Reason: "OPENAI_API_KEY=abc leaked",
	})
	// A child event with no attribution is a producer bug: dropped.
	w.Emit(Event{Type: ChildCompleted, ChildRunID: "c1", Outcome: "done", Turns: 3})
	// A valid child event (with attribution) is kept.
	w.Emit(Event{Type: ChildCompleted, ChildRunID: "c1", Chain: "main > r", Outcome: OutcomeDone, Turns: 3})

	events, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (unattributed child dropped)", len(events))
	}
	if strings.Contains(events[0].Error, "ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890") {
		t.Errorf("error text was not scrubbed: %q", events[0].Error)
	}
	if !strings.Contains(events[0].Reason, "[redacted api_key]") {
		t.Errorf("reason text was not scrubbed: %q", events[0].Reason)
	}
}

func TestWriterErrStickyOnWriteFailure(t *testing.T) {
	// A write handle that can never succeed: the pipe's write end is
	// closed, so every write fails on every platform. Building the
	// writer directly keeps this hermetic — no directory surgery that
	// breaks TempDir cleanup.
	_, wEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := wEnd.Close(); err != nil {
		t.Fatal(err)
	}
	w := &Writer{f: wEnd, runID: "run-broken"}

	w.Emit(Event{Type: RunStarted})
	if w.Err() == "" {
		t.Fatal("expected a latched write error")
	}
	// A later emission cannot unlatch the first error: evidence
	// durability once lost is not retroactively restored.
	w.Emit(Event{Type: RunCompleted, Outcome: OutcomeDone})
	if w.Err() == "" {
		t.Fatal("error must stay latched for the writer's lifetime")
	}
	_ = w.Close() // best-effort; the handle is already dead
}

func TestReadAllCorruptLineNamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.events.jsonl")
	payload := `{"seq":1,"type":"run_started"}` + "\n" + `{"seq":2,"type"` + "\n"
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := ReadAll(path)
	if err == nil {
		t.Fatal("expected error for corrupt line 2")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error = %v, want it to name line 2", err)
	}
	if len(events) != 1 {
		t.Fatalf("partial events = %d, want 1 (the readable prefix)", len(events))
	}
}

func TestReadAllMissingFile(t *testing.T) {
	if _, err := ReadAll(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestCountAndLastByChild(t *testing.T) {
	events := []Event{
		{Type: DelegationRequested},
		{Type: ChildCompleted, ChildRunID: "a", Outcome: OutcomeDone, Turns: 1},
		{Type: ChildCompleted, ChildRunID: "a", Outcome: OutcomeDone, Turns: 9},
		{Type: ChildProgress, ChildRunID: "b"},
	}
	if got := Count(events, ChildCompleted); got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
	if got := Count(events, ToolDenied); got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
	last := LastByChild(events)
	if len(last) != 1 || last["a"].Turns != 9 {
		t.Errorf("last = %+v, want only child a with turns 9", last)
	}
}

func TestSanitizeScrubsSecretShapes(t *testing.T) {
	// redact.Scrub rewrites credential-shaped text; ordinary words pass
	// through untouched. Both facts are asserted here.
	secret := "sk-12345678901234567890abcdef"
	e := Event{Type: RunFailed, Error: "boom " + secret, Reason: "api_key=" + secret, Status: "token " + secret}
	sanitize(&e)
	for name, field := range map[string]string{"error": e.Error, "reason": e.Reason, "status": e.Status} {
		if strings.Contains(field, secret) {
			t.Errorf("%s field kept the credential: %q", name, field)
		}
	}
	if !strings.Contains(e.Error, "[redacted]") {
		t.Errorf("scrub marker missing from error: %q", e.Error)
	}
	if !strings.Contains(e.Reason, "[redacted api_key]") {
		t.Errorf("scrub marker missing from reason: %q", e.Reason)
	}

	plain := Event{Type: RunFailed, Error: "plain wording", Reason: "plain wording", Status: "plain wording"}
	sanitize(&plain)
	if plain.Error != "plain wording" || plain.Reason != "plain wording" || plain.Status != "plain wording" {
		t.Errorf("non-credential text was altered: %+v", plain)
	}

	// Counters and identifiers are never touched.
	e2 := Event{Type: ChildCompleted, ChildRunID: "c", Turns: 5, ToolCalls: 2, Denied: 1}
	sanitize(&e2)
	if e2.ChildRunID != "c" || e2.Turns != 5 || e2.ToolCalls != 2 || e2.Denied != 1 {
		t.Errorf("identifier/counter fields mutated: %+v", e2)
	}
}
