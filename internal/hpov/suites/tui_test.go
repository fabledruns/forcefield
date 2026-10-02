package suites

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/markers"
)

// tuiFakeMain emulates the interactive TUI path for hermetic tests:
// it emits the required marker set to stderr (never touching the
// pty), selected by HPOV_TUI_MODE:
//
//	full          all marks, then clean exit 0 (simulates /exit quit)
//	no-ready      everything except first-useful-frame, then sleep
//	no-stage-tools  all marks except stage-tools, then clean exit 0
//	ignore-quit   all marks, then sleep through the quit grace
func tuiFakeMain() int {
	mode := os.Getenv("HPOV_TUI_MODE")
	emit := func(ev string) {
		line := "ff-perf " + ev
		if ev == "first-frame" || ev == "runtime-ready" || ev == "first-useful-frame" {
			line += " alloc=1000 sys=2000"
		}
		_, _ = os.Stderr.WriteString(line + "\n")
		time.Sleep(15 * time.Millisecond)
	}
	for _, ev := range tuiMarks {
		switch {
		case mode == "no-ready" && ev == tuiPrimaryMark:
			continue
		case mode == "no-stage-tools" && ev == "stage-tools":
			continue
		}
		emit(ev)
	}
	switch mode {
	case "no-ready", "ignore-quit":
		time.Sleep(120 * time.Second)
		return 0
	default:
		time.Sleep(300 * time.Millisecond)
		return 0
	}
}

func tuiFakeEnv(t *testing.T, mode string) {
	t.Helper()
	// Both fakes: priming uses the launch `run` mode, measurement
	// uses the bare-invocation TUI mode.
	testEnvPassthrough = map[string]string{
		"HPOV_SUITES_FAKE": "1",
		"HPOV_TUI_FAKE":    "1",
		"HPOV_TUI_MODE":    mode,
	}
}

func tuiTestSetup(t *testing.T, b *timelineBench) (bench.Fixture, bench.Subject) {
	t.Helper()
	subj := fakeSubject(t)
	fx, err := b.Setup(context.Background(), testRunEnv(t), subj)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return fx, subj
}

func TestTUIRegistration(t *testing.T) {
	found := false
	for _, r := range All() {
		if r.Bench.Spec().ID == TUIBenchmarkID {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s not registered", TUIBenchmarkID)
	}
	spec := (&timelineBench{}).Spec()
	if spec.Tier != 1 || string(spec.Kind) != "e2e" {
		t.Fatalf("tier/kind = %d/%s", spec.Tier, spec.Kind)
	}
	hasPTY := false
	for _, r := range spec.Requires {
		if r == "pty" {
			hasPTY = true
		}
	}
	if !hasPTY {
		t.Fatal("tui benchmark must require pty")
	}
	if spec.CVThreshold != 0.15 {
		t.Fatalf("CV threshold = %v, want 0.15 (tui family)", spec.CVThreshold)
	}
	if got := spec.PlanFor("standard"); got.Warmup != 3 || got.N != 20 {
		t.Fatalf("standard plan = %+v, want warmup 3 n 20", got)
	}
	if len(spec.Metrics) != len(tuiMarks)+len(tuiSegments) {
		t.Fatalf("metrics = %d, want %d", len(spec.Metrics), len(tuiMarks)+len(tuiSegments))
	}
	wantMarks := []string{"t_main_entry", "t_config_loaded", "t_runtime_init_start",
		"t_stage_skills", "t_stage_memory", "t_stage_provider", "t_stage_tools",
		"t_stage_agents", "t_first_frame", "t_first_useful_frame", "t_runtime_ready"}
	for i, w := range wantMarks {
		if spec.Metrics[i].Name != w {
			t.Fatalf("metric %d = %s, want %s", i, spec.Metrics[i].Name, w)
		}
	}
}

func TestTUIFullFlow(t *testing.T) {
	tuiFakeEnv(t, "full")
	b := &timelineBench{}
	fx, subj := tuiTestSetup(t, b)
	obs, err := b.Iterate(context.Background(), fx, subj, bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if !obs.Valid {
		t.Fatalf("full flow invalid: %s attrs=%v", obs.InvalidReason, obs.Attrs)
	}
	for _, m := range (&timelineBench{}).Spec().Metrics {
		v, ok := obs.Values[m.Name]
		if !ok {
			t.Fatalf("missing metric %s", m.Name)
		}
		if v < 0 {
			t.Fatalf("metric %s negative: %v", m.Name, v)
		}
	}
	if len(obs.Unavailable) != 0 {
		t.Fatalf("full flow should measure every metric: %v", obs.Unavailable)
	}
	// Ordering: placeholder frame precedes the useful frame precedes
	// readiness.
	tff := obs.Values["t_first_frame"]
	tuf := obs.Values["t_first_useful_frame"]
	trr := obs.Values["t_runtime_ready"]
	if !(tff <= tuf && tuf <= trr) {
		t.Fatalf("mark order violated: %v %v %v", tff, tuf, trr)
	}
	if obs.Attrs["exit_code"] != "0" || obs.Attrs["quit_method"] != "clean" {
		t.Fatalf("attrs = %v", obs.Attrs)
	}
	// Segments telescope: each equals the difference of its two
	// endpoint marks (plan §timeline invariant).
	for _, tc := range []struct{ seg, from, to string }{
		{"seg_main_entry_to_config_loaded", "t_main_entry", "t_config_loaded"},
		{"seg_config_loaded_to_runtime_init_start", "t_config_loaded", "t_runtime_init_start"},
		{"seg_stage_agents_to_runtime_ready", "t_stage_agents", "t_runtime_ready"},
		{"seg_first_frame_to_runtime_ready", "t_first_frame", "t_runtime_ready"},
	} {
		want := obs.Values[tc.to] - obs.Values[tc.from]
		if got := obs.Values[tc.seg]; !near(got, want) {
			t.Errorf("%s = %v, want %v", tc.seg, got, want)
		}
	}
}

// near compares floats with a tolerance scaled to magnitude, matching
// the schema's recomputation tolerance.
func near(a, b float64) bool { return math.Abs(a-b) <= 1e-6*(1+math.Abs(b)) }

func TestTUIMissingReady(t *testing.T) {
	tuiFakeEnv(t, "no-ready")
	b := &timelineBench{readinessTimeout: 2 * time.Second, quitGrace: time.Second}
	fx, subj := tuiTestSetup(t, b)
	obs, err := b.Iterate(context.Background(), fx, subj, bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if obs.Valid {
		t.Fatal("missing first-useful-frame must be invalid")
	}
	if !strings.Contains(obs.InvalidReason, tuiPrimaryMark) {
		t.Fatalf("reason must name the primary marker: %q", obs.InvalidReason)
	}
	// Invalid samples still carry aligned (zeroed) values so the raw
	// vector stays indexable.
	for _, m := range b.Spec().Metrics {
		if _, ok := obs.Values[m.Name]; !ok {
			t.Fatalf("invalid sample missing metric %s", m.Name)
		}
	}
}

func TestTUIMissingOptionalMarkStaysValid(t *testing.T) {
	// Only the primary readiness mark gates validity. A missing
	// optional endpoint makes its metrics unavailable, never zero.
	tuiFakeEnv(t, "no-stage-tools")
	b := &timelineBench{readinessTimeout: 5 * time.Second, quitGrace: 2 * time.Second}
	fx, subj := tuiTestSetup(t, b)
	obs, err := b.Iterate(context.Background(), fx, subj, bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if !obs.Valid {
		t.Fatalf("absent optional mark must stay valid: %s", obs.InvalidReason)
	}
	for _, name := range []string{"t_stage_tools"} {
		if _, na := obs.Unavailable[name]; !na {
			t.Errorf("%s must be unavailable, values=%v", name, obs.Values[name])
		}
		if obs.Values[name] != 0 {
			t.Errorf("%s must not carry a fabricated value: %v", name, obs.Values[name])
		}
	}
	// Metrics with both endpoints observed stay measured.
	if _, na := obs.Unavailable["t_runtime_ready"]; na {
		t.Error("runtime-ready must remain measured")
	}
	if obs.Attrs["missing_marks"] != "stage-tools" {
		t.Errorf("missing_marks = %q", obs.Attrs["missing_marks"])
	}
}

func TestTUISegmentNeedsBothEndpoints(t *testing.T) {
	base := time.Now()
	tl := markers.Build([]markers.StampedLine{
		{At: base.Add(10 * time.Millisecond), Text: "ff-perf main-entry"},
		{At: base.Add(30 * time.Millisecond), Text: "ff-perf first-useful-frame"},
	})
	values := map[string]float64{}
	unavailable := map[string]string{}
	fillTimelineValues(values, unavailable, tl, base)
	if _, ok := values["seg_main_entry_to_config_loaded"]; ok {
		t.Fatal("segment with an absent endpoint must not be reported")
	}
	if reason := unavailable["seg_main_entry_to_config_loaded"]; !strings.Contains(reason, "config-loaded") {
		t.Fatalf("unavailable reason must name the absent endpoint: %q", reason)
	}
	// Both endpoints present: measured.
	if got, want := values["seg_process_to_main_entry"], 10.0; !near(got, want) {
		t.Fatalf("spawn-anchored segment = %v, want %v", got, want)
	}
	if got, want := values["seg_process_to_first_useful_frame"], 30.0; !near(got, want) {
		t.Fatalf("spawn-anchored segment = %v, want %v", got, want)
	}
}

func TestTUIQuitKill(t *testing.T) {
	tuiFakeEnv(t, "ignore-quit")
	b := &timelineBench{quitGrace: time.Second}
	fx, subj := tuiTestSetup(t, b)
	obs, err := b.Iterate(context.Background(), fx, subj, bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if obs.Valid {
		t.Fatal("forced termination must be invalid")
	}
	if !strings.Contains(obs.InvalidReason, "forced termination") {
		t.Fatalf("reason = %q", obs.InvalidReason)
	}
}

func TestTUITimelineMath(t *testing.T) {
	base := time.Now()
	lines := []markers.StampedLine{
		{At: base.Add(20 * time.Millisecond), Text: "ff-perf main-entry"},
		{At: base.Add(50 * time.Millisecond), Text: "ff-perf config-loaded"},
		{At: base.Add(60 * time.Millisecond), Text: "ff-perf runtime-init-start"},
		{At: base.Add(70 * time.Millisecond), Text: "ff-perf stage-skills"},
		{At: base.Add(80 * time.Millisecond), Text: "ff-perf stage-memory"},
		{At: base.Add(90 * time.Millisecond), Text: "ff-perf stage-provider"},
		{At: base.Add(100 * time.Millisecond), Text: "ff-perf stage-tools"},
		{At: base.Add(150 * time.Millisecond), Text: "ff-perf stage-agents"},
		{At: base.Add(90 * time.Millisecond), Text: "ff-perf first-frame"},
		{At: base.Add(110 * time.Millisecond), Text: "ff-perf first-useful-frame"},
		{At: base.Add(200 * time.Millisecond), Text: "ff-perf runtime-ready alloc=5 sys=6"},
	}
	tl := markers.Build(lines)
	if missing := tl.Missing(tuiMarks); len(missing) != 0 {
		t.Fatalf("missing = %v", missing)
	}
	values := map[string]float64{}
	unavailable := map[string]string{}
	fillTimelineValues(values, unavailable, tl, base)
	if len(unavailable) != 0 {
		t.Fatalf("unavailable on a complete timeline: %v", unavailable)
	}
	want := map[string]float64{
		"t_main_entry": 20, "t_config_loaded": 50, "t_runtime_init_start": 60,
		"t_first_useful_frame": 110, "t_runtime_ready": 200,
		"seg_process_to_main_entry":                    20,
		"seg_main_entry_to_config_loaded":              30,
		"seg_config_loaded_to_runtime_init_start":      10,
		"seg_runtime_init_start_to_first_useful_frame": 50,
		"seg_process_to_first_useful_frame":            110,
		"seg_stage_agents_to_runtime_ready":            50,
		"seg_first_frame_to_runtime_ready":             110,
	}
	for k, w := range want {
		if got := values[k]; got < w-0.001 || got > w+0.001 {
			t.Errorf("%s = %v, want %v", k, got, w)
		}
	}
}
