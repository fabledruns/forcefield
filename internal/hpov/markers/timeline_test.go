package markers

import (
	"reflect"
	"testing"
	"time"
)

func stamped(base time.Time, texts ...string) []StampedLine {
	out := make([]StampedLine, len(texts))
	for i, tx := range texts {
		out[i] = StampedLine{At: base.Add(time.Duration(i) * 10 * time.Millisecond), Text: tx}
	}
	return out
}

func TestBuildOrderAndFirstWins(t *testing.T) {
	base := time.Now()
	tl := ffp.Build(stamped(base,
		"noise",
		"ff-perf main-entry",
		"ff-perf config-loaded",
		"ff-perf main-entry", // duplicate: first wins
		"ff-perf first-frame alloc=10 sys=20",
	))
	if !reflect.DeepEqual(tl.Order, []string{"main-entry", "config-loaded", "first-frame"}) {
		t.Fatalf("order = %v", tl.Order)
	}
	if !tl.Marks["main-entry"].Equal(base.Add(10 * time.Millisecond)) {
		t.Fatal("first occurrence must win")
	}
	if tl.Mem["first-frame"] != (MemMark{Alloc: 10, Sys: 20}) {
		t.Fatalf("mem = %+v", tl.Mem["first-frame"])
	}
}

func TestBuildDedicatedStderrPipe(t *testing.T) {
	// Markers reach the driver on the child's own stderr pipe: one per
	// line, in emission order, with a trailing CR tolerated.
	base := time.Now()
	tl := ffp.Build(stamped(base,
		"ff-perf first-frame alloc=5 sys=6\r\n",
		"ff-perf runtime-ready alloc=7 sys=8\n",
	))
	if !reflect.DeepEqual(tl.Order, []string{"first-frame", "runtime-ready"}) {
		t.Fatalf("order = %v", tl.Order)
	}
	if tl.Mem["runtime-ready"] != (MemMark{Alloc: 7, Sys: 8}) {
		t.Fatalf("mem = %+v", tl.Mem["runtime-ready"])
	}
}

func TestBuildRejectsConsoleNoise(t *testing.T) {
	// A line carrying terminal escapes before the marker is not a
	// marker line: the driver must never see console output on the
	// marker pipe.
	base := time.Now()
	tl := ffp.Build(stamped(base, "\x1b[2Kff-perf runtime-ready alloc=1 sys=2\r"))
	if len(tl.Order) != 0 {
		t.Fatalf("console noise parsed as marks: %v", tl.Order)
	}
}

func TestBuildSkipsMalformed(t *testing.T) {
	base := time.Now()
	tl := ffp.Build(stamped(base,
		"ff-perf ",
		"ff-perf foo=bar",
		"ff-perf good-name",
	))
	if !reflect.DeepEqual(tl.Order, []string{"good-name"}) {
		t.Fatalf("order = %v", tl.Order)
	}
}

func TestBuildIgnoresTSField(t *testing.T) {
	base := time.Now()
	tl := ffp.Build(stamped(base, "ff-perf main-entry t=17138300"))
	ms, ok := tl.Ms("main-entry", base)
	if !ok || ms != 0 {
		t.Fatalf("ms = %v, %v (reader clock rules, not t=)", ms, ok)
	}
}

func TestMissing(t *testing.T) {
	tl := ffp.Build(stamped(time.Now(), "ff-perf main-entry"))
	if got := tl.Missing([]string{"main-entry", "first-useful-frame"}); !reflect.DeepEqual(got, []string{"first-useful-frame"}) {
		t.Fatalf("missing = %v", got)
	}
	if got := tl.Missing([]string{"main-entry"}); len(got) != 0 {
		t.Fatalf("missing = %v", got)
	}
}

func TestMsDeltas(t *testing.T) {
	base := time.Now()
	tl := ffp.Build(stamped(base, "ff-perf a", "ff-perf b"))
	ma, _ := tl.Ms("a", base)
	mb, _ := tl.Ms("b", base)
	if ma != 0 || mb != 10 {
		t.Fatalf("marks = %v, %v", ma, mb)
	}
	if _, ok := tl.Ms("nope", base); ok {
		t.Fatal("absent mark must report missing")
	}
}
