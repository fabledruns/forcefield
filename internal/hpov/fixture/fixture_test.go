package fixture

import (
	"os"
	"strings"
	"testing"

	"forcefield/internal/hpov/bench"
)

// TestScrubEnvIsSubjectDriven pins the boundary: a variable namespace is
// scrubbed because a subject's contract names it, not because HPOV
// happens to know the name.
func TestScrubEnvIsSubjectDriven(t *testing.T) {
	t.Setenv("FF_PERF_MARKERS", "1")

	// With no contract, HPOV records no opinion about another product's
	// namespace: only its own generic rules (proxies, GIT_*) are
	// recorded as deliberately removed.
	_, removed := ScrubEnv(nil, bench.Env{})
	for _, r := range removed {
		if r == "FF_PERF_MARKERS" {
			t.Fatal("an undeclared namespace must not be recorded as scrubbed by HPOV")
		}
	}

	// The Forcefield profile's contract does declare FF_, so it is
	// removed and recorded.
	env, removed := ScrubEnv(nil, bench.Env{ScrubPrefixes: []string{"FF_"}})
	if contains(strings.Join(env, "\n"), "FF_PERF_MARKERS=") {
		t.Fatal("declared prefix must not reach the child")
	}
	found := false
	for _, r := range removed {
		if r == "FF_PERF_MARKERS" {
			found = true
		}
	}
	if !found {
		t.Fatalf("removed does not record the contract-scrubbed variable (got %v)", removed)
	}

	// A declared name is honored even when it is otherwise allowlisted,
	// which is what makes the list a contract rather than a hint.
	env, _ = ScrubEnv(nil, bench.Env{ScrubExact: []string{"PATH"}})
	if contains(strings.Join(env, "\n"), "PATH=") {
		t.Fatal("contract-declared exact name must not reach the child")
	}
}

func TestRunRootAndDirs(t *testing.T) {
	root, err := NewRunRoot("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	home, err := NewHome(root, "home-0")
	if err != nil {
		t.Fatal(err)
	}
	work, err := NewWorkDir(root, "work-0")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{root, home, work} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("%s not a dir", d)
		}
	}
}

func TestScrubEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/tmp/x")
	t.Setenv("FF_PERF_MARKERS", "1")
	t.Setenv("HTTP_PROXY", "http://proxy")
	// FF_* is scrubbed only because the subject's contract says so:
	// HPOV itself has no product to hide.
	scrub := bench.Env{ScrubPrefixes: []string{"FF_"}}
	env, removed := ScrubEnv(map[string]string{"HPOV_TEST_SET": "1"}, scrub)
	joined := ""
	for _, kv := range env {
		joined += kv + "\n"
	}
	for _, bad := range []string{"GIT_DIR=", "FF_PERF_MARKERS=", "HTTP_PROXY="} {
		if contains(joined, bad) {
			t.Fatalf("scrubbed env leaks %q", bad)
		}
	}
	if !contains(joined, "HPOV_TEST_SET=1") {
		t.Fatal("override missing from env")
	}
	if !contains(joined, "PATH=") {
		t.Fatal("PATH must survive scrubbing")
	}
	for _, want := range []string{"GIT_DIR", "FF_PERF_MARKERS", "HTTP_PROXY"} {
		found := false
		for _, r := range removed {
			if r == want {
				found = true
			}
		}
		if !found {
			t.Errorf("removed does not record %s (got %v)", want, removed)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
