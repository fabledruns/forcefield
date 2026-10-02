package markers

import (
	"reflect"
	"testing"
)

func TestEvents(t *testing.T) {
	stderr := "Created default config at X\n" +
		"ff-perf stage-skills\n" +
		"ff-perf first-frame alloc=123 sys=456\n" +
		"not a marker ff-perf fake\n" +
		"ff-perf   stage-agents   \n" +
		"ff-perf\n" +
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
	if Has(stderr, "fake") {
		t.Fatal("inline text must not parse as a marker")
	}
}
