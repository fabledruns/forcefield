package subject

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/hpov/bench"
)

func writeProfile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadProfileDefaultIsForcefield pins the compatibility guarantee:
// an invocation that names no profile behaves exactly as HPOV did before
// the extraction.
func TestLoadProfileDefaultIsForcefield(t *testing.T) {
	for _, ref := range []string{"", "builtin:forcefield", "  "} {
		p, err := LoadProfile(ref)
		if err != nil {
			t.Fatalf("LoadProfile(%q): %v", ref, err)
		}
		if p.Name != BuiltinForcefieldName || p.Contract.Product != "forcefield" {
			t.Fatalf("LoadProfile(%q) = %s/%s", ref, p.Name, p.Contract.Product)
		}
	}
	if _, err := LoadProfile("builtin:somethingelse"); err == nil {
		t.Fatal("an unknown built-in profile must be an error, not a silent default")
	}
}

// TestLoadProfileFromFile is the external-harness seam: a new harness is
// described by a JSON document, with no Go code and no HPOV rebuild.
func TestLoadProfileFromFile(t *testing.T) {
	path := writeProfile(t, `{
  "name": "harnessx",
  "contract": {
    "product": "harnessx",
    "bin": "hx",
    "marker_prefix": "hx-perf ",
    "enable_env": {"HX_TRACE": "1"},
    "version": {"args": ["--release"], "stdout_prefix": "harnessx "},
    "headless": {
      "args": ["headless", "--probe"],
      "exit_code": 3,
      "primed": true,
      "probe_checks": [
        {"name": "no_such_agent", "substring": "no agent named"},
        {"name": "boot_seen", "marker": "booted"}
      ]
    },
    "tui": {
      "primary_mark": "interactive",
      "marks": ["booted", "interactive"],
      "segments": [{"name": "seg_spawn_to_booted", "from": "\u0000spawn", "to": "booted"}],
      "quit_input": ":q",
      "require_exit_zero": true
    },
    "env": {"scrub_prefixes": ["HX_"]}
  }
}`)
	p, err := LoadProfile(path)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Name != "harnessx" || p.Source != path {
		t.Fatalf("profile = %s from %q", p.Name, p.Source)
	}
	c := p.Contract
	if c.Product != "harnessx" || c.MarkerPrefix != "hx-perf " {
		t.Fatalf("identity/prefix = %q/%q", c.Product, c.MarkerPrefix)
	}
	if got := c.Method(c.Version.Args); got != "hx --release" {
		t.Fatalf("version workload = %q", got)
	}
	if c.Headless.ExitCode != 3 || !c.Headless.Primed || len(c.Headless.ProbeChecks) != 2 {
		t.Fatalf("headless = %+v", c.Headless)
	}
	if c.TUI.PrimaryMark != "interactive" || c.TUI.QuitInput != ":q" {
		t.Fatalf("tui = %+v", c.TUI)
	}
	if len(c.TUI.Metrics()) != 3 {
		t.Fatalf("metrics = %d, want 3", len(c.TUI.Metrics()))
	}
	// A segment anchored at process start is spelled with the JSON
	// escape for bench.SpawnAnchor, so a profile document can express it.
	if c.TUI.Segments[0].From != bench.SpawnAnchor {
		t.Fatalf("segment anchor = %q, want SpawnAnchor", c.TUI.Segments[0].From)
	}
	if c.TUI.HeapFields {
		t.Fatal("a foreign profile must not claim Go heap fields")
	}
	if len(c.Env.ScrubPrefixes) != 1 || c.Env.ScrubPrefixes[0] != "HX_" {
		t.Fatalf("scrub = %v", c.Env.ScrubPrefixes)
	}
	// The built-in Forcefield profile must not have been disturbed.
	if Builtin().Contract.TUI.PrimaryMark != "first-useful-frame" {
		t.Fatal("loading a profile mutated the built-in Forcefield profile")
	}
}

// TestLoadProfileRejectsBadContracts: a malformed profile fails at the
// command line rather than silently invalidating every sample.
func TestLoadProfileRejectsBadContracts(t *testing.T) {
	cases := map[string]string{
		"no name":       `{"contract": {"product": "x"}}`,
		"no product":    `{"name": "x", "contract": {}}`,
		"unknown field": `{"name": "x", "contract": {"product": "x", "nope": 1}}`,
		"tui no prefix": `{"name": "x", "contract": {"product": "x",
			"tui": {"primary_mark": "ready", "marks": ["ready"], "quit_input": "q"}}}`,
		"tui no quit": `{"name": "x", "contract": {"product": "x", "bin": "x",
			"marker_prefix": "p ", "tui": {"primary_mark": "ready", "marks": ["ready"]}}}`,
		"marker check without prefix": `{"name": "x", "contract": {"product": "x",
			"headless": {"args": ["go"], "exit_code": 1,
			  "probe_checks": [{"name": "c", "marker": "m"}]}}}`,
	}
	for name, body := range cases {
		if _, err := LoadProfile(writeProfile(t, body)); err == nil {
			t.Errorf("%s: want an error, got nil", name)
		}
	}
	if _, err := LoadProfile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("a missing profile file must be an error")
	}
}

// TestApplyToStampsContract: every subject in a run is measured under
// the one selected profile.
func TestApplyToStampsContract(t *testing.T) {
	p := Builtin()
	got := p.ApplyTo([]bench.Subject{
		{Label: "a", Path: "/x"},
		{Label: "b", Path: "/y"},
	})
	if len(got) != 2 {
		t.Fatalf("subjects = %d", len(got))
	}
	for _, s := range got {
		if s.Contract.Product != "forcefield" {
			t.Fatalf("subject %s product = %q", s.Label, s.Contract.Product)
		}
	}
	// The input slice is not mutated: a caller's subjects stay reusable.
	if got[0].Label != "a" || got[1].Path != "/y" {
		t.Fatal("ApplyTo must not reorder or rewrite its input")
	}
}

// TestMarkerEnvNoteOnlyWhenInstrumented: provenance names the marker
// environment a contract actually turns on, and nothing otherwise.
func TestMarkerEnvNoteOnlyWhenInstrumented(t *testing.T) {
	if note := MarkerEnvNote(Builtin().Contract); note["FF_PERF_MARKERS"] != "1 (marker pass only)" {
		t.Fatalf("forcefield note = %v", note)
	}
	bare := bench.Contract{Product: "bare"}
	if note := MarkerEnvNote(bare); note != nil {
		t.Fatalf("a contract with no instrumentation must record nothing, got %v", note)
	}
	// The note is derived from the contract, not restated by the runner.
	if !strings.Contains(Builtin().Source, BuiltinForcefieldName) {
		t.Fatalf("built-in source = %q", Builtin().Source)
	}
}

func TestBuiltinContractIsValid(t *testing.T) {
	if err := Builtin().Contract.Validate(); err != nil {
		t.Fatalf("built-in contract invalid: %v", err)
	}
	if got := BuiltinProfiles(); len(got) != 1 || got[0] != BuiltinForcefieldName {
		t.Fatalf("BuiltinProfiles() = %v", got)
	}
}
