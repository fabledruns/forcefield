package orchevents

// FuzzReadEvents lives in its own file so existing test files (and their
// Gitleaks fingerprints pinned by line number) stay untouched.

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// FuzzReadEvents asserts the reader contract of ReadAll on arbitrary
// bytes shaped as a .events.jsonl stream. Headless pipelines hand real
// files here, so the invariants are the documented ones, not writer
// validation:
//
//   - No panic and no hang: a corrupt file is caller-recoverable
//     evidence, never a crash (termination is additionally enforced by
//     the fuzz engine's per-exec timeout).
//   - ReadAll is deterministic: the same file parses to the same events
//     and the same error twice.
//   - The error contract: a positioned JSON failure names its line
//     ("parse event line N of path"); a scanner-level failure (e.g. a
//     line beyond the 1 MiB line cap) names the file ("read path").
//     Either way the readable prefix comes back for the caller to
//     decide on partial evidence.
//   - The reader never invents records: the returned event count never
//     exceeds the number of non-empty input lines.
//   - The downstream in-package consumers (Count, Truncated,
//     LastByChild) stay consistent with whatever was parsed.
//
// Writer-side drop rules (valid(), validOutcome) are deliberately NOT
// asserted here: the reader is the forward-compatible surface that must
// keep accepting what an older or newer writer produced, including
// unknown types and fields.
func FuzzReadEvents(f *testing.F) {
	// Fixed run/child ids keep the seeds byte-stable and minimal.
	const (
		runID   = "d5c95a40-1111-4222-8333-444455556666"
		childID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	)
	// The writer's real wire shapes (Event with omitempty fields).
	validSeries := `{"seq":1,"ts":"2026-11-04T10:00:00.123456789Z","run":"` + runID + `","type":"run_started"}
{"seq":2,"ts":"2026-11-04T10:00:01Z","run":"` + runID + `","type":"agent_spawned","agent":"researcher","chain":"root > researcher","child_run":"` + childID + `"}
{"seq":3,"ts":"2026-11-04T10:00:02Z","run":"` + runID + `","type":"child_progress","agent":"researcher","chain":"root > researcher","child_run":"` + childID + `","call_id":"call-1","turns":1}
{"seq":4,"ts":"2026-11-04T10:00:03Z","run":"` + runID + `","type":"tool_started","agent":"researcher","chain":"root > researcher","child_run":"` + childID + `","call_id":"call-1","tool":"echo"}
{"seq":5,"ts":"2026-11-04T10:00:04Z","run":"` + runID + `","type":"budget_exhausted","agent":"researcher","chain":"root > researcher","child_run":"` + childID + `","call_id":"call-2","budget":"total_turns","budget_limit":4,"budget_value":5,"reason":"aggregate child-turn budget exhausted (5 turns across all children, limit 4)"}
{"seq":6,"ts":"2026-11-04T10:00:05Z","run":"` + runID + `","type":"child_completed","agent":"researcher","chain":"root > researcher","child_run":"` + childID + `","outcome":"done","turns":3,"tool_calls":2}
{"seq":7,"ts":"2026-11-04T10:00:06Z","run":"` + runID + `","type":"run_completed","status":"verified"}`

	seeds := []string{
		// Empty input and boundary-only files: no records, no error.
		"",
		"\n",
		"\n\n\n",
		" \n",
		// One valid minimal record, with and without the trailing newline.
		`{"seq":1,"ts":"2026-11-04T10:00:00Z","run":"` + runID + `","type":"run_started"}`,
		`{"seq":1,"ts":"2026-11-04T10:00:00Z","run":"` + runID + `","type":"run_started"}` + "\n",
		// A full realistic writer stream.
		validSeries,
		validSeries + "\n",
		// Blank-line separation between valid records.
		"\n\n" + validSeries + "\n\n\n",
		// Record-boundary shapes the scanner must place honestly: CRLF
		// pairs leave a \r inside the last token (a parse error by
		// contract, never a crash), and two records sharing one line
		// parse as one corrupt line.
		"\r\n",
		validSeries_first() + "\r\n" + validSeries_first(),
		`{"seq":1,"type":"run_started"}{"seq":2,"type":"run_completed"}`,
		// Forward compatibility: unknown types and unknown fields must
		// parse; the writer-side schema gate does not apply to reads.
		`{"seq":9,"type":"future_event_added_in_v2"}`,
		`{"seq":9,"type":"tool_started","future_string":"x","future_nested":{"a":[1,2,{"b":null}]},"meta":{"tool":"shell","exit":0}}`,
		// null and empty-object roots decode as zero-value events; fields
		// absent (no type at all) still parse on the read path.
		"null",
		"{}",
		`{"seq":4}`,
		// Counter and pointer fields at unusual shapes: negative counts,
		// exit_code null, giant-but-fitting integers.
		`{"seq":5,"type":"child_completed","turns":-3,"tool_calls":-1}`,
		`{"seq":5,"type":"tool_completed","exit_code":null}`,
		`{"seq":5,"type":"run_failed","exit_code":9223372036854775807,"duration_ms":-9223372036854775808}`,
		// Malformed and truncated JSON: empty objects, stray tokens,
		// mid-key and mid-string truncations like a crash/disk-full tail.
		"{",
		"}",
		"not json at all",
		`"just a string"`,
		`{"seq":2,"type"`,
		`{"seq":2,"type"}` + "\n" + validSeries_first(),
		validSeries_first() + "\n" + `{"seq":2,"ts":"2026-11-04T10:00:01Z","type":"child_progress","tu`,
		// Wrong field types and overflow: int64 overflow, string into a
		// numeric field, object into a pointer field, float overflow in
		// a map (1e400 overflows float64).
		`{"seq":"1","type":"run_started"}`,
		`{"seq":12,"type":13}`,
		`{"seq":12345678901234567890123,"type":"run_started"}`,
		`{"seq":3,"type":"child_progress","turns":"many"}`,
		`{"seq":3,"type":"tool_completed","exit_code":{"n":1}}`,
		`{"type":"run_completed","meta":{"ms":1e400}}`,
		`{"type":"run_completed","meta":[1,2,"x"]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	// One oversized corpus entry, built at runtime so the test binary
	// stays small: a single JSONL line beyond the reader's 1 MiB line
	// cap. The scanner must bound it into the "read" error path without
	// panicking or inflating memory.
	f.Add([]byte(`{"seq":1,"ts":"2026-11-04T10:00:00Z","type":"run_started","pad":"` + strings.Repeat("x", 1<<20) + `"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "fuzz.events.jsonl")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write fuzz input: %v", err)
		}

		// Determinism: the same file must parse to the same events and
		// the same error time after time.
		events1, err1 := ReadAll(path)
		events2, err2 := ReadAll(path)
		if !reflect.DeepEqual(events1, events2) {
			t.Fatalf("ReadAll nondeterministic events on %.120q:\nfirst %+v\nthen  %+v", data, events1, events2)
		}
		if msg1, msg2 := text(err1), text(err2); msg1 != msg2 {
			t.Fatalf("ReadAll nondeterministic error on %.120q: %q then %q", data, msg1, msg2)
		}

		if err1 != nil {
			// Documented error contract: the positioned parse failure
			// names its line; the scanner-level failure names the file.
			msg := err1.Error()
			if !strings.Contains(msg, "parse event line ") && !strings.HasPrefix(msg, "read ") {
				t.Fatalf("error %q is neither a positioned parse error nor a read error for %.120q", msg, data)
			}
		}

		// The reader never invents records: at most one event per
		// non-empty input line (empty lines and the scan-bound tail are
		// not events, and a positioned error keeps the prefix honest).
		nonEmpty := 0
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			if len(line) > 0 {
				nonEmpty++
			}
		}
		if len(events1) > nonEmpty {
			t.Fatalf("returned %d events from %d non-empty lines on %.120q", len(events1), nonEmpty, data)
		}

		// The in-package consumers stay consistent with the parsed
		// slice: no panic, and the child-run summary never exceeds the
		// ChildCompleted events it is built from.
		_ = Truncated(events1)
		if got := len(LastByChild(events1)); got > Count(events1, ChildCompleted) {
			t.Fatalf("LastByChild produced %d entries from %d child_completed events on %.120q", got, Count(events1, ChildCompleted), data)
		}
	})
}

// validSeries_first returns the first record of the realistic series;
// used by boundary seeds that splice partial records around it.
func validSeries_first() string {
	return `{"seq":1,"ts":"2026-11-04T10:00:00Z","run":"d5c95a40-1111-4222-8333-444455556666","type":"run_started"}`
}

// text normalizes the nil case so two calls can be compared by string.
func text(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
