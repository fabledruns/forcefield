// TUI startup timeline: the interactive `ff` binary under a fixed
// 120x40 pty, measured from process spawn through the marker
// timeline to runtime readiness.
//
// Boundaries (all reader-timestamped marker receipts, ms from
// T_spawn, per M3: markers carry no timestamps, so the reader clock
// on receipt is the timing source for every phase delta):
//
//	UI branch:      T_spawn -> main-entry -> config-loaded ->
//	                first-frame -> first-useful-frame
//	Runtime branch: runtime-init-start -> stage-skills -> stage-memory ->
//	                stage-provider -> stage-tools -> stage-agents ->
//	                runtime-ready (rt_finalize)
//
// first-useful-frame is the primary readiness boundary: never the
// placeholder View, never process creation, never a fixed sleep. Once
// it is observed the run tears down by writing /exit to the pty; the
// timeline is collected until runtime-ready, so teardown never cuts
// the measurement short.
//
// Marks (11): main-entry, config-loaded, runtime-init-start,
// stage-skills, stage-memory, stage-provider, stage-tools,
// stage-agents, first-frame, first-useful-frame, runtime-ready.
// stage-mcp is excluded by design (0 MCP servers in the fixture);
// stage-session is ignored (M2: it measures the EventMem STW, not a
// stage).
//
// Validity rests on the primary readiness boundary only:
// first-useful-frame. A missing optional mark does NOT invalidate the
// sample — the metrics whose endpoints are absent are reported as
// unavailable with a reason and excluded from that metric's
// statistics, never zero-filled (plan §9.1: unavailable data is a
// null plus a reason, never 0).
package suites

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/fixture"
	"forcefield/internal/hpov/markers"
	"forcefield/internal/hpov/pty"
)

// TUIBenchmarkID is the registered benchmark name.
const TUIBenchmarkID = "tui.startup.timeline"

// tuiPrimaryMark is the primary readiness boundary: the first frame
// that can accept a task. Never the placeholder View, never process
// creation, never a fixed sleep. Only its absence invalidates a run.
const tuiPrimaryMark = "first-useful-frame"

// tuiMarks is the full mark set this benchmark reports on.
var tuiMarks = []string{
	"main-entry", "config-loaded", "runtime-init-start",
	"stage-skills", "stage-memory", "stage-provider", "stage-tools",
	"stage-agents", "first-frame", tuiPrimaryMark, "runtime-ready",
}

// DefaultTUIReadiness is the bound for the full required set. It is
// a failure detector, not synchronization: marks arrive in ~200ms.
const DefaultTUIReadiness = 60 * time.Second

// DefaultTUIQuitGrace bounds clean /exit teardown before forced kill.
const DefaultTUIQuitGrace = 10 * time.Second

// DefaultTUIKeySettle is the pause between the quit text and its
// Enter, so the two are delivered as separate key events rather than
// one paste.
const DefaultTUIKeySettle = 150 * time.Millisecond

// DefaultTUITail bounds the wait for runtime-ready once the primary
// readiness mark is observed. runtime-ready fires after the runtime
// finishes building — provider probe and MCP startup included — which
// measured tens of ms on a warm home but can reach seconds when those
// lookups are slow. This is a failure detector, not synchronization:
// absent marks become unavailable metrics rather than invalid samples.
const DefaultTUITail = 10 * time.Second

// TUIBenchmarks returns the TUI suite constructors (steady only:
// shared primed home, fresh workdir per iteration).
func TUIBenchmarks() []bench.Benchmark {
	return []bench.Benchmark{&timelineBench{}}
}

type timelineBench struct {
	readinessTimeout time.Duration // 0 = DefaultTUIReadiness
	quitGrace        time.Duration // 0 = DefaultTUIQuitGrace
}

func (b *timelineBench) readiness() time.Duration {
	if b.readinessTimeout > 0 {
		return b.readinessTimeout
	}
	return DefaultTUIReadiness
}

func (b *timelineBench) grace() time.Duration {
	if b.quitGrace > 0 {
		return b.quitGrace
	}
	return DefaultTUIQuitGrace
}

func (b *timelineBench) Spec() bench.Spec {
	metrics := make([]bench.MetricSpec, 0, len(tuiMarks)+len(tuiSegments))
	for _, ev := range tuiMarks {
		metrics = append(metrics, bench.MetricSpec{
			Name: markMetric(ev), Unit: "ms", Direction: bench.LowerIsBetter,
		})
	}
	for _, s := range tuiSegments {
		metrics = append(metrics, bench.MetricSpec{
			Name: s.name, Unit: "ms", Direction: bench.LowerIsBetter,
		})
	}
	return bench.Spec{
		ID:                TUIBenchmarkID,
		DefinitionVersion: 1,
		Title:             "Interactive startup timeline (steady)",
		Purpose: "User-perceived interactive startup under a fixed 120x40 pty: " +
			"first render, first useful frame, runtime readiness, with the " +
			"runtime-branch stage marks. Steady = primed isolated home, fresh " +
			"workdir per iteration. Teardown writes /exit once readiness is " +
			"observed; forced kills fail the sample.",
		Kind:        bench.KindE2E,
		Tier:        1,
		Metrics:     metrics,
		Requires:    []string{"pty"},
		CVThreshold: 0.15,
		Params: map[string]string{
			"workload": "ff (interactive, pty 120x40, TERM=xterm-256color)",
			"profile":  "steady", "repo": "none", "mcp_servers": "0",
		},
		Predicate: "primary readiness mark first-useful-frame observed and clean /exit quit with exit_code==0; absent optional marks yield unavailable metrics, not zeros",
		Plans: map[string]bench.Plan{
			"quick":    {Warmup: 1, N: 4, TimeoutSec: 120},
			"standard": {Warmup: 3, N: 20, TimeoutSec: 120},
			"full":     {Warmup: 3, N: 50, TimeoutSec: 120},
		},
	}
}

func (b *timelineBench) Setup(ctx context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	parent, err := launchParent(env.Root, TUIBenchmarkID, subj)
	if err != nil {
		return bench.Fixture{}, err
	}
	home, _, err := freshDirs(parent, "shared-home")
	if err != nil {
		return bench.Fixture{}, err
	}
	// Prime the isolated home with the real default config via the
	// headless workload (untimed). The TUI needs config.yaml to
	// exist; how it gets there is not what this benchmark measures.
	primeWork, err := fixture.NewWorkDir(parent, "prime-work")
	if err != nil {
		return bench.Fixture{}, err
	}
	if err := primeHeadlessHome(ctx, subj.Path, home, primeWork, planTimeout(b.Spec(), env.Profile)); err != nil {
		return bench.Fixture{}, err
	}
	return bench.Fixture{HomeDir: home, WorkDir: parent, Timeout: planTimeout(b.Spec(), env.Profile)}, nil
}

func (b *timelineBench) Iterate(ctx context.Context, fx bench.Fixture, subj bench.Subject, it bench.Iter) (bench.Observation, error) {
	// Fresh workdir per iteration (untimed creation): no session or
	// trace accumulation across iterations.
	_, work, err := freshDirs(fx.WorkDir, fmt.Sprintf("w-%d", it.Index))
	if err != nil {
		return bench.Observation{Valid: false, InvalidReason: err.Error()}, err
	}
	return b.runOnce(ctx, fx, subj, work)
}

func (b *timelineBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

// tuiEnv builds the scrubbed pty environment: home isolation, fixed
// terminal contract, markers on (the timeline pass is the only pass).
func tuiEnv(home string) []string {
	set := fixture.HomeEnv(home)
	set["FF_PERF_MARKERS"] = "1"
	set["TERM"] = pty.Term
	set["COLUMNS"] = "120"
	set["LINES"] = "40"
	for k, v := range testEnvPassthrough {
		set[k] = v
	}
	env, _ := fixture.ScrubEnv(set)
	return env
}

// runOnce spawns bare `ff` on the pty and measures to readiness.
func (b *timelineBench) runOnce(ctx context.Context, fx bench.Fixture, subj bench.Subject, work string) (bench.Observation, error) {
	obs := bench.Observation{
		Values:      map[string]float64{},
		Attrs:       map[string]string{},
		Unavailable: map[string]string{},
	}
	fail := func(reason string) (bench.Observation, error) {
		obs.Valid = false
		obs.InvalidReason = reason
		fillMissingTUI(obs.Values)
		return obs, nil
	}

	tSpawn := time.Now()
	child, err := pty.Start(pty.Options{
		Path: subj.Path, Env: tuiEnv(fx.HomeDir), Dir: work,
		Cols: pty.DefaultCols, Rows: pty.DefaultRows,
	})
	if err != nil {
		return fail("pty start: " + err.Error())
	}
	// Cleanup is unconditional: no orphaned ff processes.
	defer func() { _ = child.Close() }()

	// Markers arrive on the child's own stderr pipe; the console stream
	// is drained separately so TUI rendering can never stall on a full
	// buffer.
	var ptyBytes atomic.Int64
	go drainToNull(child.Output(), &ptyBytes)
	src := child.Stderr()
	lines, eof, timedOut, readErr := collectTimeline(src, b.readiness(), &ptyBytes)
	tl := markers.Build(lines)

	// Teardown: polite /exit first (valid even pre-ready), forced
	// kill on grace expiry. Teardown failure fails the sample:
	// clean termination is part of the validity contract.
	//
	// The text and the Enter arrive as separate writes: one combined
	// write is delivered to the terminal as a single paste, where the
	// CR is inserted as a newline instead of submitting.
	quitMethod := "clean"
	_, _ = child.WriteInput([]byte("/exit"))
	time.Sleep(DefaultTUIKeySettle)
	_, _ = child.WriteInput([]byte("\r"))
	exitCode, ok := waitBounded(child, b.grace())
	if !ok {
		_ = child.Kill()
		quitMethod = "kill-after-quit-timeout"
		exitCode, _ = waitBounded(child, 15*time.Second)
	}
	if ctx.Err() != nil {
		return fail("run aborted: " + ctx.Err().Error())
	}

	obs.Attrs["exit_code"] = itoa(uint64(exitCode))
	obs.Attrs["quit_method"] = quitMethod
	obs.Attrs["pty_bytes"] = itoa(uint64(ptyBytes.Load()))
	if readErr != nil {
		// The marker stream ended on a read error (a truncated
		// timeline), which is a harness fault, not an absent mark.
		obs.Attrs["read_error"] = readErr.Error()
		return fail("marker stream read failed: " + readErr.Error())
	}
	if missing := tl.Missing(tuiMarks); len(missing) > 0 {
		obs.Attrs["missing_marks"] = strings.Join(missing, ",")
	}
	fillTimelineValues(obs.Values, obs.Unavailable, tl, tSpawn)

	_, ready := tl.Ms(tuiPrimaryMark, tSpawn)
	switch {
	case timedOut && !ready:
		return fail("readiness timeout (" + b.readiness().String() +
			"); primary marker " + tuiPrimaryMark + " never observed")
	case !ready:
		return fail("primary readiness marker " + tuiPrimaryMark +
			" missing" + eofSuffix(eof, missingReason(tl, tuiPrimaryMark)))
	case quitMethod != "clean":
		return fail("forced termination after quit grace")
	case exitCode != 0:
		return fail(fmt.Sprintf("unexpected exit %d after /exit quit", exitCode))
	}
	obs.Valid = true
	return obs, nil
}

// eofSuffix notes early EOF only when it explains the missing mark.
func eofSuffix(eof bool, missing string) string {
	if eof && missing != "" {
		return " (child EOF before " + missing + ")"
	}
	return ""
}

func missingReason(tl markers.Timeline, ev string) string {
	if _, ok := tl.Marks[ev]; !ok {
		return ev
	}
	return ""
}

// collectTimeline stamps and collects marker lines until the primary
// readiness mark is seen plus the timeline tail (runtime-ready), EOF,
// or the readiness deadline. Marks observed are never discarded
// mid-run: an absent mark stays absent and becomes an unavailable
// metric rather than a fabricated zero.
//
// The reader goroutine keeps draining to EOF after the main loop
// stops consuming, so a child can never stall on a full buffer.
func collectTimeline(src *os.File, timeout time.Duration, nbytes *atomic.Int64) (lines []markers.StampedLine, eof, timedOut bool, readErr error) {
	lineCh := make(chan markers.StampedLine, 64)
	errCh := make(chan error, 1)
	go func() {
		defer close(lineCh)
		br := bufio.NewReader(&countedReader{r: src, n: nbytes})
		for {
			line, rerr := br.ReadString('\n')
			if line != "" {
				// Stamped on receipt: markers carry no in-process
				// timestamps (M3), so the reader clock is the timing
				// source for every phase delta.
				select {
				case lineCh <- markers.StampedLine{At: time.Now(), Text: line}:
				default:
					// Main stopped consuming: keep draining, drop parsing.
				}
			}
			if rerr != nil {
				if rerr != io.EOF {
					errCh <- rerr
				}
				return
			}
		}
	}()
	have := map[string]bool{}
	// deadline bounds primary readiness; tail bounds the post-readiness
	// wait for runtime-ready so a release that never emits it cannot
	// stretch every iteration by the full readiness timeout.
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var tailC <-chan time.Time
	complete := func() bool { return have[tuiPrimaryMark] && have["runtime-ready"] }
	// drainErr pulls the reader's terminal error without blocking.
	drainErr := func() error {
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}
	for {
		select {
		case l, ok := <-lineCh:
			if !ok {
				return lines, true, false, drainErr()
			}
			lines = append(lines, l)
			if ev, ok := markers.Event(l.Text); ok {
				have[ev] = true
			}
			if have[tuiPrimaryMark] && tailC == nil {
				tailC = time.After(DefaultTUITail)
			}
			if complete() {
				return lines, false, false, drainErr()
			}
		case err := <-errCh:
			// A read failure ends collection: report it so a truncated
			// timeline is visible instead of looking like absent marks.
			return lines, false, false, err
		case <-deadline.C:
			return lines, false, true, drainErr()
		case <-tailC:
			return lines, false, false, drainErr()
		}
	}
}

// drainToNull consumes a stream to EOF (unix pty output side).
func drainToNull(r io.Reader, nbytes *atomic.Int64) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			nbytes.Add(int64(n))
		}
		if err != nil {
			return
		}
	}
}

type countedReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n.Add(int64(n))
	}
	return n, err
}

// waitBounded waits for child exit with a bound.
func waitBounded(child interface {
	Wait() (int, error)
}, d time.Duration) (int, bool) {
	type res struct {
		code int
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		code, err := child.Wait()
		ch <- res{code, err}
	}()
	select {
	case r := <-ch:
		_ = r.err
		return r.code, true
	case <-time.After(d):
		return -1, false
	}
}

// tuiSegment is one reported delta: a segment exists only when both
// endpoints are observed (plan §"Unavailable data is null plus a
// reason, never 0").
type tuiSegment struct {
	name string
	from string
	to   string
}

// spawnFrom marks a segment anchored at T_spawn rather than at a
// marker endpoint.
const spawnFrom = "\x00spawn"

var tuiSegments = []tuiSegment{
	// Spawn-anchored segments: measured from T_spawn, so they use the
	// spawn timestamp rather than a marker endpoint.
	{"seg_process_to_main_entry", spawnFrom, "main-entry"},
	{"seg_main_entry_to_config_loaded", "main-entry", "config-loaded"},
	{"seg_config_loaded_to_runtime_init_start", "config-loaded", "runtime-init-start"},
	{"seg_runtime_init_start_to_first_useful_frame", "runtime-init-start", tuiPrimaryMark},
	{"seg_process_to_first_useful_frame", spawnFrom, tuiPrimaryMark},
	{"seg_stage_agents_to_runtime_ready", "stage-agents", "runtime-ready"},
	{"seg_first_frame_to_runtime_ready", "first-frame", "runtime-ready"},
}

// markMetric is the metric name for a mark's process-relative time.
func markMetric(ev string) string { return "t_" + strings.ReplaceAll(ev, "-", "_") }

// fillTimelineValues computes every owned metric from the timeline.
// A metric with a missing endpoint is recorded in unavailable (with the
// absent endpoint named) instead of being zero-filled: the runner keeps
// the raw vector aligned but drops those samples from that metric's
// statistics.
func fillTimelineValues(values map[string]float64, unavailable map[string]string, tl markers.Timeline, tSpawn time.Time) {
	mark := func(ev string) {
		ms, ok := tl.Ms(ev, tSpawn)
		if !ok {
			unavailable[markMetric(ev)] = "marker " + ev + " not observed"
			return
		}
		values[markMetric(ev)] = ms
	}
	for _, ev := range tuiMarks {
		mark(ev)
	}
	seg := func(s tuiSegment) {
		from, oka := tSpawn, true
		if s.from != spawnFrom {
			from, oka = tl.Marks[s.from]
		}
		to, okc := tl.Marks[s.to]
		switch {
		case !oka && !okc:
			unavailable[s.name] = "endpoints " + s.from + " and " + s.to + " not observed"
		case !oka:
			unavailable[s.name] = "endpoint " + s.from + " not observed"
		case !okc:
			unavailable[s.name] = "endpoint " + s.to + " not observed"
		default:
			values[s.name] = float64(to.Sub(from).Nanoseconds()) / 1e6
		}
	}
	for _, s := range tuiSegments {
		seg(s)
	}
}

// fillMissingTUI zeroes every owned metric for invalid samples so
// values arrays stay aligned with measure iterations. Invalid samples
// are excluded from statistics wholesale, so these zeros are
// placeholders, never reported data.
func fillMissingTUI(values map[string]float64) {
	for _, ev := range tuiMarks {
		if _, ok := values[markMetric(ev)]; !ok {
			values[markMetric(ev)] = 0
		}
	}
	for _, s := range tuiSegments {
		if _, ok := values[s.name]; !ok {
			values[s.name] = 0
		}
	}
}
