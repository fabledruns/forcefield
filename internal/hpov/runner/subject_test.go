package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/subject"
)

// TestSubjectPathIsNormalizedBeforeWorkdirChange is the regression test
// for the relative-path defect: benchmarks hand the child their own
// working directory, so a subject path that is still relative when that
// happens resolves against the fixture instead of the caller and the
// spawn fails.
func TestSubjectPathIsNormalizedBeforeWorkdirChange(t *testing.T) {
	dir := t.TempDir()
	// Copy nothing: a path is enough, because normalization happens
	// before anything is spawned.
	link := filepath.Join(dir, "subject-copy")
	if err := os.WriteFile(link, []byte("not really a binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	pr, err := subject.Probe("rel", link, subject.Builtin().Contract, "local-build")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !filepath.IsAbs(pr.Path) {
		t.Fatalf("probed path = %q, want absolute", pr.Path)
	}

	// A path written the way a user would type it must normalize too.
	t.Chdir(dir)
	rel, err := subject.Probe("rel", "subject-copy", subject.Builtin().Contract, "local-build")
	if err != nil {
		t.Fatalf("probe relative: %v", err)
	}
	if !filepath.IsAbs(rel.Path) {
		t.Fatalf("relative subject path stayed relative: %q", rel.Path)
	}
	if rel.Subject.Binary.Basename != "subject-copy" {
		t.Fatalf("basename = %q", rel.Subject.Binary.Basename)
	}
	// Both spellings must resolve to the same artifact, or A/A runs
	// would compare different files.
	if rel.Subject.Binary.SHA256 != pr.Subject.Binary.SHA256 {
		t.Fatal("the same file probed by two spellings must hash identically")
	}
}

// workdirBench records the working directory it was handed and whether
// the subject path still resolves from there.
type workdirBench struct {
	dir    string
	gotDir string
	gotAbs bool
}

func (f *workdirBench) Spec() bench.Spec {
	return bench.Spec{
		ID: "test.workdir", DefinitionVersion: 1, Tier: 1, Kind: bench.KindE2E,
		Metrics:   []bench.MetricSpec{{Name: "ok", Unit: "count", Direction: bench.HigherIsBetter}},
		Predicate: "subject path resolves from the fixture workdir",
	}
}

func (f *workdirBench) Setup(_ context.Context, _ *bench.RunEnv, _ bench.Subject) (bench.Fixture, error) {
	return bench.Fixture{WorkDir: f.dir}, nil
}

func (f *workdirBench) Iterate(_ context.Context, fx bench.Fixture, subj bench.Subject, _ bench.Iter) (bench.Observation, error) {
	f.gotDir = fx.WorkDir
	if !filepath.IsAbs(subj.Path) {
		return bench.Observation{}, nil
	}
	// Resolve the subject the way a spawn would, from the fixture dir.
	_, err := os.Stat(filepath.Join(fx.WorkDir, subj.Path))
	f.gotAbs = err == nil || filepath.IsAbs(subj.Path)
	return bench.Observation{Values: map[string]float64{"ok": 1}, Valid: true}, nil
}

func (f *workdirBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

func TestRunnerHandsBenchmarksAnAbsoluteSubjectPath(t *testing.T) {
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	n, w := 1, 0
	fb := &workdirBench{dir: dir}
	out := filepath.Join(t.TempDir(), "r.json")
	oc, err := Run(context.Background(), Config{
		Benchmarks:     []bench.Benchmark{fb},
		Subjects:       []bench.Subject{{Label: "s", Path: self, Contract: subject.Builtin().Contract}},
		SubjectProfile: subject.Builtin(),
		Select:         []string{"test.workdir"},
		Tier:           -1,
		Profile:        "quick",
		N:              &n,
		Warmup:         &w,
		Seed:           424242,
		Out:            out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fb.gotDir != dir {
		t.Fatalf("benchmark workdir = %q, want %q", fb.gotDir, dir)
	}
	if b := oc.Result.Benchmarks[0]; b.Status != schema.StatusOK {
		t.Fatalf("status = %s, want ok: %+v", b.Status, b.StatusDetail)
	}
}

// TestProvenanceDescribesTheActualProfile pins requirement 13: marker
// provenance is recorded only when the selected contract turns markers
// on, and a subject's identity is the profile's.
func TestProvenanceDescribesTheActualProfile(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	n, w := 1, 0

	// The Forcefield profile records its marker environment, exactly as
	// before the extraction.
	ff, err := Run(context.Background(), Config{
		Benchmarks:     []bench.Benchmark{&recordBench{tier: 1}},
		Subjects:       []bench.Subject{{Label: "ff", Path: self}},
		SubjectProfile: subject.Builtin(),
		Select:         []string{"test.fake"},
		Tier:           -1, Profile: "quick", N: &n, Warmup: &w,
		Seed: 424242, Out: filepath.Join(t.TempDir(), "ff.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	set := ff.Result.Environment.EnvOverrides.Set
	if set["FF_PERF_MARKERS"] != "1 (marker pass only)" {
		t.Fatalf("forcefield provenance lost its marker note: %v", set)
	}
	if ff.Result.Run.SubjectProfile != "forcefield" {
		t.Fatalf("subject profile = %q", ff.Result.Run.SubjectProfile)
	}
	if ff.Result.Subjects[0].Product != "forcefield" {
		t.Fatalf("product = %q", ff.Result.Subjects[0].Product)
	}

	// A subject whose profile declares no instrumentation records no
	// marker environment: HPOV does not describe an unrelated subject
	// with another product's variables.
	other := subject.Profile{Name: "harnessx", Source: "test", Contract: bench.Contract{
		Product: "harnessx", Bin: "hx",
	}}
	hx, err := Run(context.Background(), Config{
		Benchmarks:     []bench.Benchmark{&recordBench{tier: 1}},
		Subjects:       other.ApplyTo([]bench.Subject{{Label: "hx", Path: self}}),
		SubjectProfile: other,
		Select:         []string{"test.fake"},
		Tier:           -1, Profile: "quick", N: &n, Warmup: &w,
		Seed: 424242, Out: filepath.Join(t.TempDir(), "hx.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hx.Result.Environment.EnvOverrides.Set {
		if strings.HasPrefix(k, "FF_") {
			t.Fatalf("foreign provenance carries %s=%s", k, v)
		}
	}
	if hx.Result.Subjects[0].Product != "harnessx" {
		t.Fatalf("product = %q, want harnessx", hx.Result.Subjects[0].Product)
	}
	if hx.Result.Run.SubjectProfile != "harnessx" {
		t.Fatalf("subject profile = %q", hx.Result.Run.SubjectProfile)
	}
}
