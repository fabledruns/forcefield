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

// Events returns ff-perf event names in line order from captured
// stderr text.
func Events(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, Prefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, Prefix))
		if rest == "" {
			continue
		}
		// Event is the first field; alloc=/sys= follow.
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			rest = rest[:i]
		}
		out = append(out, rest)
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
