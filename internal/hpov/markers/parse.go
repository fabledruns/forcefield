// Package markers parses an instrumentation marker protocol: lines of
// the form `<prefix><event>[ alloc=<n> sys=<n>]` on a child's stderr.
//
// The prefix is supplied by the subject's contract rather than fixed
// here, so one parser serves every subject. Forcefield's own prefix is
// declared in the Forcefield subject profile, not in this package.
//
// Markers carry no timestamps; the external driver timestamps each
// line on receipt. The full mark/segment timeline (telescoping
// segments, DAG branches) lands with the TUI suite; this file holds
// the minimal event parser the launch probes need.
package markers

import "strings"

// Protocol is one marker line format, discriminated by prefix.
type Protocol struct {
	Prefix string
}

// Event extracts the event name from one marker line, or ok=false for
// a non-marker line or a malformed event token.
//
// Markers arrive on the child's own stderr pipe, so a line that starts
// with the prefix is a marker: inline text merely containing the prefix
// is not, and neither is a payload-less or key=value one.
func (p Protocol) Event(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if p.Prefix == "" || !strings.HasPrefix(line, p.Prefix) {
		return "", false
	}
	// Event is the first field; alloc=/sys=/t= follow.
	rest := strings.TrimSpace(strings.TrimPrefix(line, p.Prefix))
	rest, _, _ = strings.Cut(rest, " ")
	if !isEventToken(rest) {
		return "", false
	}
	return rest, true
}

// Events returns marker event names in line order from captured
// stderr text.
func (p Protocol) Events(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		ev, ok := p.Event(line)
		if !ok {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// Has reports whether event was emitted at least once.
func (p Protocol) Has(stderr, event string) bool {
	for _, e := range p.Events(stderr) {
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
