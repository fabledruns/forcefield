package bench

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, id string
		want        bool
	}{
		{"launch.*", "launch.version", true},
		{"launch.*", "mem.headless", false},
		{"*", "launch.version", true}, // trailing * matches the subtree
		{"launch.*.steady", "launch.headless-init.steady", true},
		{"launch.headless-init.steady", "launch.headless-init.steady", true},
		{"mcp.init.*", "mcp.init.n4", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.id); got != c.want {
			t.Errorf("Match(%q,%q) = %v, want %v", c.pattern, c.id, got, c.want)
		}
	}
	if !MatchAny(nil, "anything.at.all") {
		t.Error("empty globs must match everything")
	}
	if MatchAny([]string{"launch.*"}, "mem.x") {
		t.Error("non-matching select must not match")
	}
}

func TestPlanFor(t *testing.T) {
	s := Spec{}
	if got := s.PlanFor("standard"); got.N != 30 || got.Warmup != 5 {
		t.Fatalf("standard plan = %+v", got)
	}
	if got := s.PlanFor("bogus"); got.N != 30 {
		t.Fatalf("unknown profile must fall back to standard: %+v", got)
	}
	s.Plans = map[string]Plan{"standard": {Warmup: 1, N: 2, TimeoutSec: 5}}
	if got := s.PlanFor("standard"); got.N != 2 {
		t.Fatalf("spec override ignored: %+v", got)
	}
}
