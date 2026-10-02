// Package markers parses the ff-perf marker protocol: lines of the
// form `ff-perf <event>[ alloc=<n> sys=<n>]` on stderr.
//
// Markers carry no timestamps; the external driver timestamps each
// line on receipt. The full mark/segment timeline (telescoping
// segments, DAG branches) lands with the TUI suite; this file holds
// the minimal event parser the launch probes need.
package markers

import "strings"

// Prefix is the marker line discriminator on stderr.
const Prefix = "ff-perf "

// Event extracts the event name from one marker line, or ok=false for
// a non-marker line or a malformed event token.
//
// Markers arrive on the child's own stderr pipe, so a line that starts
// with the prefix is a marker: inline text merely containing "ff-perf"
// is not, and neither is a payload-less or key=value one.
func Event(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, Prefix) {
		return "", false
	}
	// Event is the first field; alloc=/sys=/t= follow.
	rest := strings.TrimSpace(strings.TrimPrefix(line, Prefix))
	rest, _, _ = strings.Cut(rest, " ")
	if !isEventToken(rest) {
		return "", false
	}
	return rest, true
}

// Events returns ff-perf event names in line order from captured
// stderr text.
func Events(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		ev, ok := Event(line)
		if !ok {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// Has reports whether event was emitted at least once.
func Has(stderr, event string) bool {
	for _, e := range Events(stderr) {
		if e == event {
			return true
		}
	}
	return false
}

// isEventToken reports whether s is a valid event name: letters,
// digits, dash, underscore. It rejects payloads like "foo=bar".
func isEventToken(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t=") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
