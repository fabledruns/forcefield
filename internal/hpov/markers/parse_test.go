package markers

import (
	"reflect"
	"testing"
)

func TestEvents(t *testing.T) {
	stderr := "Created default config at X\n" +
		"ff-perf stage-skills\n" +
		"ff-perf first-frame alloc=123 sys=456\n" +
		"ff-perf   stage-agents   \n" +
		"ff-perf\n" +
		"ff-perf foo=bar\n" +
		"Error: unknown agent\n"
	want := []string{"stage-skills", "first-frame", "stage-agents"}
	if got := Events(stderr); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !Has(stderr, "stage-agents") {
		t.Fatal("stage-agents must be found")
	}
	if Has(stderr, "stage-mcp") {
		t.Fatal("stage-mcp must not be found")
	}
	// Malformed tokens never become events: an empty payload and a
	// key=value field are both rejected.
	if Has(stderr, "") || Has(stderr, "foo=bar") {
		t.Fatal("malformed marker payloads must not parse")
	}
}

func TestEventsRequiresOwnLine(t *testing.T) {
	// Markers arrive on the child's own stderr pipe, so the prefix must
	// start the line. Console output that merely contains the prefix is
	// not a marker.
	if got := Events("Forcefield harness saw ff-perf runtime-ready\n"); len(got) != 0 {
		t.Fatalf("inline prefix parsed: %v", got)
	}
	// Padding around the payload is tolerated.
	if got := Events("ff-perf   stage-tools   \n"); !reflect.DeepEqual(got, []string{"stage-tools"}) {
		t.Fatalf("padded marker = %v", got)
	}
}
