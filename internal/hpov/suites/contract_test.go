package suites

import (
	"context"
	"os"
	"strings"
	"testing"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/markers"
	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/subject"
)

// The suite tests drive a scripted fake that impersonates Forcefield, so
// they run against the built-in Forcefield profile rather than a copy of
// its contract. A second family of tests below uses a foreign profile to
// prove the boundary holds in both directions.

// ffContract is the built-in Forcefield contract.
var ffContract = subject.Builtin().Contract

// ffProto, ffPrimary, ffMarks and ffSegments read the Forcefield
// protocol and topology from that profile, so the tests cannot drift
// away from it.
var (
	ffProto   = markers.Protocol{Prefix: ffContract.MarkerPrefix}
	ffPrimary = ffContract.TUI.PrimaryMark
	ffMarks   = ffContract.TUI.Marks
)

// ffSubject is a subject measured under the Forcefield profile with the
// given extra environment forwarded to the fake child.
func ffSubject(label, path string, extra map[string]string) bench.Subject {
	c := ffContract
	c.Env.Passthrough = extra
	return bench.Subject{Label: label, Path: path, Contract: c}
}

// foreignContract is a second, deliberately different harness: its own
// product name, command name, flags, output text, boundary exit,
// marker protocol, mark names and quit input.
func foreignContract() bench.Contract {
	return bench.Contract{
		Product:      "harnessx",
		Bin:          "hx",
		MarkerPrefix: "hx-perf ",
		EnableEnv:    map[string]string{"HX_TRACE": "1"},
		Version:      bench.Probe{Args: []string{"--release"}, StdoutPrefix: "harnessx "},
		Headless: bench.Headless{
			Args:     []string{"headless", "--probe"},
			ExitCode: 3,
			ProbeChecks: []bench.ProbeCheck{
				{Name: "no_such_agent", Substring: `no agent named "nobody"`},
				{Name: "boot_seen", Marker: "booted"},
			},
		},
		TUI: bench.TUI{
			PrimaryMark: "interactive",
			Marks:       []string{"booted", "interactive"},
			Segments: []bench.Segment{
				{Name: "seg_spawn_to_booted", From: bench.SpawnAnchor, To: "booted"},
			},
			QuitInput:       ":q",
			RequireExitZero: true,
		},
		Env: bench.Env{ScrubPrefixes: []string{"HX_"}},
	}
}

// TestBuiltinForcefieldProfilePreservesTheContract pins the Forcefield
// side of the boundary: the profile still declares exactly the commands,
// output, exit status, markers and environment HPOV measured before the
// extraction. If this changes, Forcefield benchmark numbers are no
// longer comparable with earlier runs.
func TestBuiltinForcefieldProfilePreservesTheContract(t *testing.T) {
	p := subject.Builtin()
	c := p.Contract
	if p.Name != "forcefield" || c.Product != "forcefield" {
		t.Fatalf("identity = %s/%s, want forcefield", p.Name, c.Product)
	}
	if got := c.Method(c.Version.Args); got != "ff --version" {
		t.Fatalf("version workload = %q", got)
	}
	if c.Version.StdoutPrefix != "ff version" {
		t.Fatalf("version prefix = %q", c.Version.StdoutPrefix)
	}
	if got := c.Method(c.Help.Args); got != "ff --help" {
		t.Fatalf("help workload = %q", got)
	}
	if c.Help.StdoutContains != "Usage:" {
		t.Fatalf("help predicate = %q", c.Help.StdoutContains)
	}
	if got := c.Method(c.Headless.Args); got != "ff run --agent __bench_bogus__ x" {
		t.Fatalf("headless workload = %q", got)
	}
	if c.Headless.ExitCode != 1 || !c.Headless.Primed {
		t.Fatalf("headless boundary = exit %d primed=%v, want 1/true",
			c.Headless.ExitCode, c.Headless.Primed)
	}
	if len(c.Headless.ProbeChecks) != 2 {
		t.Fatalf("probe checks = %d, want 2", len(c.Headless.ProbeChecks))
	}
	if c.MarkerPrefix != "ff-perf " {
		t.Fatalf("marker prefix = %q", c.MarkerPrefix)
	}
	if c.TUI.PrimaryMark != "first-useful-frame" || len(c.TUI.Marks) != 11 {
		t.Fatalf("tui boundary = %q with %d marks", c.TUI.PrimaryMark, len(c.TUI.Marks))
	}
	if c.EnableEnv["FF_PERF_MARKERS"] != "1" {
		t.Fatalf("marker enable env = %v", c.EnableEnv)
	}
	if c.TUI.QuitInput != "/exit" || !c.TUI.RequireExitZero || !c.TUI.HeapFields {
		t.Fatalf("tui teardown = %+v", c.TUI)
	}
	if len(c.Env.ScrubPrefixes) != 1 || c.Env.ScrubPrefixes[0] != "FF_" {
		t.Fatalf("env scrub = %v", c.Env.ScrubPrefixes)
	}
	// The recorded validity predicates keep their wording, so results
	// stay readable against earlier runs.
	if got := (&versionBench{}).SpecFor(ffSubject("s", "p", nil)).Predicate; got !=
		"exit_code==0 and stdout starts with 'ff version'" {
		t.Fatalf("version predicate = %q", got)
	}
	if got := (&timelineBench{}).SpecFor(ffSubject("s", "p", nil)).Predicate; !strings.HasPrefix(got,
		"primary readiness mark first-useful-frame observed and clean /exit quit with exit_code==0") {
		t.Fatalf("tui predicate = %q", got)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("built-in contract invalid: %v", err)
	}
}

// TestArtifactSizeNeedsNoProfile is the subject-independent benchmark:
// it works for any executable under any contract, including none.
func TestArtifactSizeNeedsNoProfile(t *testing.T) {
	b := &artifactSizeBench{}
	payload := []byte("not-a-harness-at-all")
	p := fakeBinary(t, payload)
	subj := bench.Subject{Label: "anything", Path: p} // zero contract
	fx, err := b.Setup(context.Background(), testRunEnv(t), subj)
	if err != nil {
		t.Fatalf("setup with no contract: %v", err)
	}
	obs, err := b.Iterate(context.Background(), fx, subj, bench.Iter{Phase: bench.PhaseMeasure})
	if err != nil || !obs.Valid {
		t.Fatalf("artifact sample rejected without a contract: %v %+v", err, obs)
	}
	if obs.Values["size_bytes"] != float64(len(payload)) {
		t.Fatalf("size = %v, want %d", obs.Values["size_bytes"], len(payload))
	}
	// Its spec must not name a workload, because it has none.
	if w, ok := b.Spec().Params["workload"]; ok {
		t.Fatalf("artifact-size must declare no workload, got %q", w)
	}
}

// TestForeignProfileDrivesTheSuites is the architectural test: a
// different harness is measurable from its profile alone, with no
// Forcefield-specific knowledge in the generic path.
func TestForeignProfileDrivesTheSuites(t *testing.T) {
	subj := bench.Subject{Label: "hx", Path: fakeBinary(t, []byte("x")), Contract: foreignContract()}

	// The version benchmark runs the foreign command and applies the
	// foreign stdout predicate.
	spec := (&versionBench{}).SpecFor(subj)
	if spec.Params["workload"] != "hx --release" {
		t.Fatalf("version workload = %q", spec.Params["workload"])
	}
	if spec.Predicate != "exit_code==0 and stdout starts with 'harnessx '" {
		t.Fatalf("version predicate = %q", spec.Predicate)
	}

	// The headless benchmark uses the foreign args, boundary exit and
	// probe checks.
	hspec := (&headlessBench{}).SpecFor(subj)
	if hspec.Params["workload"] != "hx headless --probe" {
		t.Fatalf("headless workload = %q", hspec.Params["workload"])
	}
	if !strings.Contains(hspec.Predicate, "exit_code==3") {
		t.Fatalf("headless predicate = %q", hspec.Predicate)
	}
	if !strings.Contains(hspec.Predicate, "boot_seen") {
		t.Fatalf("headless probe checks not in predicate: %q", hspec.Predicate)
	}

	// The TUI metrics come from the foreign mark set, not Forcefield's.
	tspec := (&timelineBench{}).SpecFor(subj)
	want := []string{"t_booted", "t_interactive", "seg_spawn_to_booted"}
	if len(tspec.Metrics) != len(want) {
		t.Fatalf("tui metrics = %d, want %d (%v)", len(tspec.Metrics), len(want), tspec.Metrics)
	}
	for i, w := range want {
		if tspec.Metrics[i].Name != w {
			t.Fatalf("metric %d = %q, want %q", i, tspec.Metrics[i].Name, w)
		}
	}
	if tspec.Params["readiness_mark"] != "interactive" {
		t.Fatalf("readiness mark = %q", tspec.Params["readiness_mark"])
	}
	if tspec.Params["marker_prefix"] != "hx-perf " {
		t.Fatalf("marker prefix = %q", tspec.Params["marker_prefix"])
	}
	// No Forcefield mark may appear anywhere in a foreign subject's spec.
	for _, m := range tspec.Metrics {
		if strings.Contains(m.Name, "useful_frame") || strings.Contains(m.Name, "stage_") {
			t.Fatalf("foreign spec leaked a Forcefield metric: %s", m.Name)
		}
	}
	if err := subj.Contract.Validate(); err != nil {
		t.Fatalf("foreign contract invalid: %v", err)
	}
}

// TestMissingContractIsUnsupportedNotMeasured: a subject whose profile
// omits a workload gets an explicit unsupported state. It is never
// measured against a workload it did not declare, and never reported
// with zero-filled metrics.
func TestMissingContractIsUnsupportedNotMeasured(t *testing.T) {
	// A subject that declares only artifact-size-able facts: no version
	// command, no headless workload, no interactive instrumentation.
	bare := bench.Subject{
		Label:    "bare",
		Path:     fakeBinary(t, []byte("x")),
		Contract: bench.Contract{Product: "bare"},
	}
	env := testRunEnv(t)
	cases := []struct {
		name  string
		setup func(context.Context, *bench.RunEnv, bench.Subject) (bench.Fixture, error)
	}{
		{"launch.version", (&versionBench{}).Setup},
		{"launch.help", (&helpBench{}).Setup},
		{"launch.headless-init.steady", (&headlessBench{}).Setup},
		{"tui.startup.timeline", (&timelineBench{}).Setup},
		{"mem.tui.ready-rss", (&memReadyRSSBench{}).Setup},
		{"mem.tui.go-heap", (&memGoHeapBench{}).Setup},
		{"mem.headless.peak-rss", (&memHeadlessBench{}).Setup},
	}
	for _, tc := range cases {
		_, err := tc.setup(context.Background(), env, bare)
		se, ok := err.(*bench.SkipError)
		if !ok {
			t.Fatalf("%s: want SkipError, got %v", tc.name, err)
		}
		if se.Status != schema.StatusUnsupported {
			t.Fatalf("%s: status = %s, want %s", tc.name, se.Status, schema.StatusUnsupported)
		}
		if se.Code != schema.ErrSubjectContract {
			t.Fatalf("%s: code = %s, want %s", tc.name, se.Code, schema.ErrSubjectContract)
		}
		if !strings.Contains(se.Detail, "bare") {
			t.Fatalf("%s: detail does not name the subject: %q", tc.name, se.Detail)
		}
	}

	// The same subject still measures what needs no contract.
	ab := &artifactSizeBench{}
	fx, err := ab.Setup(context.Background(), env, bare)
	if err != nil {
		t.Fatalf("artifact-size must not require a contract: %v", err)
	}
	if obs, err := ab.Iterate(context.Background(), fx, bare, bench.Iter{}); err != nil || !obs.Valid {
		t.Fatalf("artifact-size sample rejected: %v %+v", err, obs)
	}
}

// TestGoHeapIsImplementationSpecific: the Go heap metrics exist only for
// a contract that declares runtime heap fields. An interactive contract
// without them yields unsupported, not zeros.
func TestGoHeapIsImplementationSpecific(t *testing.T) {
	interactive := foreignContract() // interactive, but no heap fields
	if interactive.TUI.HeapFields {
		t.Fatal("foreign profile must not claim Go heap fields")
	}
	subj := bench.Subject{Label: "hx", Path: fakeBinary(t, []byte("x")), Contract: interactive}
	_, err := (&memGoHeapBench{}).Setup(context.Background(), testRunEnv(t), subj)
	se, ok := err.(*bench.SkipError)
	if !ok || se.Status != schema.StatusUnsupported {
		t.Fatalf("go-heap must be unsupported without heap_fields, got %v", err)
	}
	if !strings.Contains(se.Detail, "heap_fields") {
		t.Fatalf("detail must name the missing contract field: %q", se.Detail)
	}
	// The interactive contract itself is sufficient for the generic pty
	// families: only the runtime-level metric is scoped away.
	if err := requireTUI(subj); err != nil {
		t.Fatalf("a foreign interactive contract must satisfy the pty benchmarks: %v", err)
	}
	// Forcefield declares them, so it keeps the metric.
	spec := (&memGoHeapBench{}).SpecFor(ffSubject("ff", "p", nil))
	if spec.Params["metric_scope"] != "implementation-specific (go runtime)" {
		t.Fatalf("go-heap must declare its scope: %v", spec.Params)
	}
}

// TestIdentityComesFromTheContract: subject identity is the profile's,
// so an arbitrary executable is never labelled as another product.
func TestIdentityComesFromTheContract(t *testing.T) {
	p := fakeBinary(t, []byte("payload"))
	foreign, err := subject.Probe("hx", p, foreignContract(), "local-build")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if foreign.Subject.Product != "harnessx" {
		t.Fatalf("product = %q, want harnessx", foreign.Subject.Product)
	}
	ff, err := subject.Probe("ff", p, subject.Builtin().Contract, "local-build")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if ff.Subject.Product != "forcefield" {
		t.Fatalf("product = %q, want forcefield", ff.Subject.Product)
	}
}

// fakeBinary writes an inert file that stands in for a subject
// executable. Benchmarks that only stat or hash it need nothing more.
func fakeBinary(t *testing.T, payload []byte) string {
	t.Helper()
	p := t.TempDir() + string(os.PathSeparator) + "subject-under-test"
	if err := os.WriteFile(p, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}
