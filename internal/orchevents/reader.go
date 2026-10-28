package orchevents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ReadAll parses every event line of the log at path, in file order
// (which is seq order: the writer assigns a gap-free sequence per file
// under one mutex). A trailing empty line from a crash between the
// final newline-buffer is tolerated; a corrupt or truncated middle
// line returns the error with context naming the line number — callers
// decide whether partial evidence is usable. Honest interruption
// handling beats silently skipping evidence.
func ReadAll(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []Event
	sc := bufio.NewScanner(f)
	// One event line stays small (no content fields), but leave headroom
	// for forward-compatible additions.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return events, fmt.Errorf("parse event line %d of %s: %w", line, path, err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		return events, fmt.Errorf("read %s: %w", path, err)
	}
	return events, nil
}

// Count returns the number of events in the log with the given type.
func Count(events []Event, t Type) int {
	n := 0
	for _, e := range events {
		if e.Type == t {
			n++
		}
	}
	return n
}

// LastByChild returns the final ChildCompleted event for each child run
// id, in spawn order. Acceptance tests assert on this summary shape.
func LastByChild(events []Event) map[string]Event {
	out := make(map[string]Event)
	for _, e := range events {
		if e.Type == ChildCompleted && e.ChildRunID != "" {
			out[e.ChildRunID] = e
		}
	}
	return out
}
