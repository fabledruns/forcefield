package markers

import (
	"reflect"
	"testing"
)

// The marker prefix is data, so one parser serves every subject. These
// tests pin both the Forcefield protocol and the fact that a different
// protocol yields different events from the same bytes.
var ffp = Protocol{Prefix: "ff-perf "}

func TestEvents(t *testing.T) {
	stderr := "Created default config at X\n" +
		"ff-perf stage-skills\n" +
		"ff-perf first-frame alloc=123 sys=456\n" +
		"ff-perf   stage-agents   \n" +
		"ff-perf\n" +
		"ff-perf foo=bar\n" +
		"Error: unknown agent\n"
	want := []string{"stage-skills", "first-frame", "stage-agents"}
	if got := ffp.Events(stderr); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !ffp.Has(stderr, "stage-agents") {
		t.Fatal("stage-agents must be found")
	}
	if ffp.Has(stderr, "stage-mcp") {
		t.Fatal("stage-mcp must not be found")
	}
	// Malformed tokens never become events: an empty payload and a
	// key=value field are both rejected.
	if ffp.Has(stderr, "") || ffp.Has(stderr, "foo=bar") {
		t.Fatal("malformed marker payloads must not parse")
	}
}

func TestEventsRequiresOwnLine(t *testing.T) {
	// Markers arrive on the child's own stderr pipe, so the prefix must
	// start the line. Console output that merely contains the prefix is
	// not a marker.
	if got := ffp.Events("Forcefield harness saw ff-perf runtime-ready\n"); len(got) != 0 {
		t.Fatalf("inline prefix parsed: %v", got)
	}
	// Padding around the payload is tolerated.
	if got := ffp.Events("ff-perf   stage-tools   \n"); !reflect.DeepEqual(got, []string{"stage-tools"}) {
		t.Fatalf("padded marker = %v", got)
	}
}

// TestPrefixIsAParameter is the subject boundary: a subject with a
// different marker protocol must be parseable without touching this
// package, and must not inherit another subject's events.
func TestPrefixIsAParameter(t *testing.T) {
	line := "hx-perf booted\nff-perf stage-agents\n"
	other := Protocol{Prefix: "hx-perf "}

	if got := other.Events(line); !reflect.DeepEqual(got, []string{"booted"}) {
		t.Fatalf("other protocol events = %v, want [booted]", got)
	}
	if got := ffp.Events(line); !reflect.DeepEqual(got, []string{"stage-agents"}) {
		t.Fatalf("forcefield protocol events = %v, want [stage-agents]", got)
	}
	// An empty prefix parses nothing rather than treating every line
	// as a marker.
	if _, ok := (Protocol{}).Event("ff-perf stage-agents"); ok {
		t.Fatal("empty prefix must not match")
	}
}
