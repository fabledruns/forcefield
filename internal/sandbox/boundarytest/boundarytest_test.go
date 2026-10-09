package boundarytest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestParseRequired(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"landlock", []string{"landlock"}},
		{"landlock,netns", []string{"landlock", "netns"}},
		{" landlock , netns ", []string{"landlock", "netns"}},
		{"landlock,,netns", []string{"landlock", "netns"}},
		{"Landlock", []string{"Landlock"}},
	}
	for _, tc := range cases {
		got := parseRequired(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseRequired(%q) has %d entries, want %d (%v)", tc.in, len(got), len(tc.want), got)
			continue
		}
		for _, w := range tc.want {
			if !got[w] {
				t.Errorf("parseRequired(%q) missing %q (%v)", tc.in, w, got)
			}
		}
	}
	// Matching is exact: "Landlock" must not satisfy "landlock".
	if parseRequired("Landlock")["landlock"] {
		t.Error("capability matching must be case-sensitive")
	}
}

func TestIsRequiredEmptyEnvRequiresNothing(t *testing.T) {
	t.Setenv("FF_REQUIRE_BOUNDARY", "")
	if IsRequired("landlock") {
		t.Error("IsRequired(landlock) = true with empty FF_REQUIRE_BOUNDARY, want false")
	}
	t.Setenv("FF_REQUIRE_BOUNDARY", "landlock, netns")
	if !IsRequired("landlock") || !IsRequired("netns") {
		t.Error("IsRequired missed an entry listed in FF_REQUIRE_BOUNDARY")
	}
	if IsRequired("pidns") {
		t.Error("IsRequired(pidns) = true, want false (not listed)")
	}
}

func TestSkipMarkerFormat(t *testing.T) {
	got := SkipMarker("landlock", "test reason")
	want := "SKIP(boundary-unavailable:landlock): test reason"
	if got != want {
		t.Errorf("SkipMarker = %q, want %q (CI logs are grepped for this exact shape)", got, want)
	}
	// The marker must name the capability: a format that drops it would
	// make skips undiagnosable.
	if !strings.Contains(SkipMarker("netns", "r"), "netns") {
		t.Error("SkipMarker output must contain the capability name")
	}
}

// TestRequireBoundarySkipFormat pins the gate's skip path: with nothing
// required, RequireBoundary must skip (not return, not fail). The exact
// message wording is pinned separately by TestSkipMarkerFormat, since a
// parent test cannot capture a subtest's Skipf output.
func TestRequireBoundarySkipFormat(t *testing.T) {
	t.Run("skip-marker", func(t *testing.T) {
		t.Setenv("FF_REQUIRE_BOUNDARY", "")
		RequireBoundary(t, "landlock", "test reason")
		// Unreachable when the gate works: RequireBoundary must skip.
		t.Fatal("RequireBoundary did not skip")
	})
}

// TestRequireBoundaryRequiredFails pins the fail-closed branch by
// actually executing it: the gate runs in a child test process with
// FF_REQUIRE_BOUNDARY listing the capability, and the parent asserts the
// child failed (not skipped, not passed) with the fail-closed marker. If
// RequireBoundary regressed to always skip, the child would exit 0 and
// this test would fail.
func TestRequireBoundaryRequiredFails(t *testing.T) {
	if os.Getenv("FF_BOUNDARY_CHILD") == "1" {
		RequireBoundary(t, "landlock", "child-process check")
		// Unreachable when the gate works: required mode must fail.
		t.Fatal("RequireBoundary did not fail in required mode")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRequireBoundaryRequiredFails$", "-test.v")
	cmd.Env = withEnv("FF_REQUIRE_BOUNDARY=landlock", "FF_BOUNDARY_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child process exited 0; want non-zero failure in required mode (output:\n%s)", out)
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() == 0 {
		t.Fatalf("child error = %v; want a non-zero test failure (output:\n%s)", err, out)
	}
	if !strings.Contains(string(out), "boundary required but unavailable") {
		t.Fatalf("child output missing the fail-closed marker (output:\n%s)", out)
	}
	if strings.Contains(string(out), "SKIP(boundary-unavailable") && strings.Contains(string(out), "--- SKIP") {
		t.Fatalf("child skipped instead of failing in required mode (output:\n%s)", out)
	}
}

// withEnv returns the current environment with the given K=V pairs
// replacing any existing entries, so child processes get deterministic
// inputs regardless of the parent's ambient environment (appending
// would leave duplicate keys whose winner is platform-dependent).
func withEnv(extra ...string) []string {
	replace := make(map[string]string, len(extra))
	for _, kv := range extra {
		name, value, _ := strings.Cut(kv, "=")
		replace[name] = value
	}
	out := make([]string, 0, len(os.Environ())+len(replace))
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !isReplaced(replace, name) {
			out = append(out, kv)
		}
	}
	for name, value := range replace {
		out = append(out, name+"="+value)
	}
	return out
}

func isReplaced(replace map[string]string, name string) bool {
	_, ok := replace[name]
	return ok
}

// TestRequireBoundaryRejectsUnknownCapability pins fail-closed naming:
// a typo'd capability must fail even when nothing is required, so the
// mistake surfaces instead of silently skipping required mode.
func TestRequireBoundaryRejectsUnknownCapability(t *testing.T) {
	if os.Getenv("FF_BOUNDARY_CHILD") == "1" {
		RequireBoundary(t, "landlok", "typo check")
		t.Fatal("RequireBoundary accepted an unknown capability")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRequireBoundaryRejectsUnknownCapability$", "-test.v")
	cmd.Env = withEnv("FF_REQUIRE_BOUNDARY=", "FF_BOUNDARY_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("child process exited 0; want failure for unknown capability (output:\n%s)", out)
	}
	if !strings.Contains(string(out), "unknown capability") {
		t.Fatalf("child output missing the unknown-capability marker (output:\n%s)", out)
	}
}
