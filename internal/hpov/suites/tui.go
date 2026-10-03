// TUI startup timeline: an interactive subject under a fixed 120x40
// pty, measured from process spawn through the subject's marker
// timeline to its readiness boundary.
//
// The marker protocol, the mark names, the readiness mark and the quit
// sequence all come from the subject's contract; this file contains no
// harness's marker names. A subject whose contract declares no
// interactive instrumentation is reported unsupported, not measured
// against marks it will never emit.
//
// Boundaries (all reader-timestamped marker receipts, ms from
// T_spawn, per M3: markers carry no timestamps, so the reader clock
// on receipt is the timing source for every phase delta):
//
//	every mark in the contract's reported set is a metric anchored at
//	T_spawn, and every contract segment is a delta between two marks or
//	from T_spawn itself.
//
// The contract's primary mark is the readiness boundary: never process
// creation, never a fixed sleep. Once observed, the run tears down by
// writing the contract's quit input to the pty, and collection continues
// to the contract's tail mark so teardown never cuts the measurement
// short.
//
// Validity rests on the primary readiness boundary only. A missing
// optional mark does NOT invalidate the sample — the metrics whose
// endpoints are absent are reported as unavailable with a reason and
// excluded from that metric's statistics, never zero-filled (plan §9.1:
// unavailable data is a null plus a reason, never 0).
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

// DefaultTUIReadiness is the bound for the full required set. It is
// a failure detector, not synchronization: marks arrive in ~200ms.
const DefaultTUIReadiness = 60 * time.Second

// DefaultTUIQuitGrace bounds clean teardown before forced kill.
const DefaultTUIQuitGrace = 10 * time.Second

// DefaultTUIKeySettle is the pause between the quit text and its
// submit key, so the two are delivered as separate key events rather
// than one paste.
const DefaultTUIKeySettle = 150 * time.Millisecond

// DefaultTUITail bounds the wait for the contract's tail mark once the
// primary readiness mark is observed. A tail mark fires after the
// subject finishes building — provider probe and subsystem startup
// included — which measured tens of ms on a warm home but can reach
// seconds when those lookups are slow. This is a failure detector, not
// synchronization: absent marks become unavailable metrics rather than
// invalid samples.
const DefaultTUITail = 10 * time.Second

// TUIBenchmarks returns the TUI suite constructors (steady only:
// shared primed home, fresh workdir per iteration).
func TUIBenchmarks() []bench.Benchmark {
	return []bench.Benchmark{&timelineBench{}}
}

// proto is the subject's marker parser.
func proto(c bench.Contract) markers.Protocol {
	return markers.Protocol{Prefix: c.MarkerPrefix}
}

// requireTUI reports the pty benchmarks unsupported when the subject's
// contract declares no usable interactive instrumentation. A missing
// contract is never papered over with zero metrics.
func requireTUI(subj bench.Subject) error {
	if !subj.Contract.TUI.Defined() {
		return subj.Unsupported("an interactive (pty) workload contract")
	}
	if missing := subj.Contract.TUI.Missing(); len(missing) > 0 {
		return subj.Unsupported("interactive contract fields: " + strings.Join(missing, ", "))
	}
	if subj.Contract.MarkerPrefix == "" {
		return subj.Unsupported("an instrumentation marker prefix")
	}
	return nil
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

func (b *timelineBench) Spec() bench.Spec { return b.SpecFor(bench.Subject{}) }

// SpecFor derives the benchmark's metrics from the subject's marker set:
// one metric per reported mark, one per declared segment. Two subjects
// with different marks are comparable only where their marks coincide,
// instead of being forced into one harness's vocabulary.
func (b *timelineBench) SpecFor(subj bench.Subject) bench.Spec {
	c := subj.Contract
	return bench.Spec{
		ID:                TUIBenchmarkID,
		DefinitionVersion: 1,
		Title:             "Interactive startup timeline (steady)",
		Purpose: "User-perceived interactive startup under a fixed 120x40 pty: " +
			"the subject's mark timeline to its readiness boundary (" +
			c.TUI.ReadinessPhrase() + "). Steady = primed isolated home, " +
			"fresh workdir per iteration. Teardown writes the contract's quit " +
			"input once readiness is observed; forced kills fail the sample.",
		Kind:        bench.KindE2E,
		Tier:        1,
		Metrics:     c.TUI.Metrics(),
		Requires:    []string{"pty"},
		CVThreshold: 0.15,
		Params: map[string]string{
			"workload": c.Method(c.TUI.Args) + " (interactive, pty 120x40, TERM=" + pty.Term + ")",
			"profile":  "steady", "repo": "none", "mcp_servers": "0",
			"marker_prefix":  c.MarkerPrefix,
			"readiness_mark": c.TUI.PrimaryMark,
		},
		Predicate: "primary " + c.TUI.ReadinessPhrase() + " observed and clean " +
			c.TUI.QuitInput + " quit with exit_code==0; absent optional marks " +
			"yield unavailable metrics, not zeros",
		Plans: map[string]bench.Plan{
			"quick":    {Warmup: 1, N: 4, TimeoutSec: 120},
			"standard": {Warmup: 3, N: 20, TimeoutSec: 120},
			"full":     {Warmup: 3, N: 50, TimeoutSec: 120},
		},
	}
}

func (b *timelineBench) Setup(ctx context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	if err := requireTUI(subj); err != nil {
		return bench.Fixture{}, err
	}
	parent, err := launchParent(env.Root, TUIBenchmarkID, subj)
	if err != nil {
		return bench.Fixture{}, err
	}
	home, _, err := freshDirs(parent, "shared-home")
	if err != nil {
		return bench.Fixture{}, err
	}
	// Prime the isolated home with the subject's real default state via
	// the headless workload (untimed). The TUI needs that state to
	// exist; how it gets there is not what this benchmark measures.
	primeWork, err := fixture.NewWorkDir(parent, "prime-work")
	if err != nil {
		return bench.Fixture{}, err
	}
	if err := primeHeadlessHome(ctx, subj, home, primeWork, planTimeout(b.Spec(), env.Profile)); err != nil {
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

// tuiEnv builds the scrubbed pty environment: home isolation, a fixed
// terminal contract, and the subject's own instrumentation enabled (the
// timeline pass is the only pass that needs it).
func tuiEnv(subj bench.Subject, home string) []string {
	set := fixture.HomeEnv(home)
	for k, v := range subj.Contract.EnableEnv {
		set[k] = v
	}
	set["TERM"] = pty.Term
	set["COLUMNS"] = "120"
	set["LINES"] = "40"
	for k, v := range subj.Contract.Env.Passthrough {
		set[k] = v
	}
	env, _ := fixture.ScrubEnv(set, subj.Contract.Env)
	return env
}

// runOnce spawns the subject on the pty and measures to readiness.
func (b *timelineBench) runOnce(ctx context.Context, fx bench.Fixture, subj bench.Subject, work string) (bench.Observation, error) {
	c := subj.Contract
	obs := bench.Observation{
		Values:      map[string]float64{},
		Attrs:       map[string]string{},
		Unavailable: map[string]string{},
	}
	fail := func(reason string) (bench.Observation, error) {
		obs.Valid = false
		obs.InvalidReason = reason
		fillMissingTUI(obs.Values, c.TUI)
		return obs, nil
	}

	tSpawn := time.Now()
	child, err := pty.Start(pty.Options{
		Path: subj.Path, Args: c.TUI.Args, Env: tuiEnv(subj, fx.HomeDir), Dir: work,
		Cols: pty.DefaultCols, Rows: pty.DefaultRows,
	})
	if err != nil {
		return fail("pty start: " + err.Error())
	}
	// Cleanup is unconditional: no orphaned child processes.
	defer func() { _ = child.Close() }()

	// Markers arrive on the child's own stderr pipe; the console stream
	// is drained separately so TUI rendering can never stall on a full
	// buffer.
	var ptyBytes atomic.Int64
	go drainToNull(child.Output(), &ptyBytes)
	src := child.Stderr()
	lines, eof, timedOut, readErr := collectTimeline(src, b.readiness(), &ptyBytes, c)
	tl := proto(c).Build(lines)

	// Teardown: polite quit first (valid even pre-ready), forced
	// kill on grace expiry. Teardown failure fails the sample:
	// clean termination is part of the validity contract.
	//
	// The quit text and its submit key arrive as separate writes: one
	// combined write is delivered to the terminal as a single paste,
	// where the CR is inserted as a newline instead of submitting.
	quitMethod := "clean"
	quitText, quitKey := c.TUI.Submit()
	_, _ = child.WriteInput([]byte(quitText))
	time.Sleep(DefaultTUIKeySettle)
	_, _ = child.WriteInput([]byte(quitKey))
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
	if missing := tl.Missing(c.TUI.Marks); len(missing) > 0 {
		obs.Attrs["missing_marks"] = strings.Join(missing, ",")
	}
	fillTimelineValues(obs.Values, obs.Unavailable, tl, tSpawn, c.TUI)

	_, ready := tl.Ms(c.TUI.PrimaryMark, tSpawn)
	switch {
	case timedOut && !ready:
		return fail("readiness timeout (" + b.readiness().String() +
			"); " + c.TUI.ReadinessPhrase() + " never observed")
	case !ready:
		return fail("primary " + c.TUI.ReadinessPhrase() +
			" missing" + eofSuffix(eof, missingReason(tl, c.TUI.PrimaryMark)))
	case quitMethod != "clean":
		return fail("forced termination after quit grace")
	case c.TUI.RequireExitZero && exitCode != 0:
		return fail(fmt.Sprintf("unexpected exit %d after the quit sequence", exitCode))
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

// collectTimeline stamps and collects marker lines until the contract's
// readiness mark is seen plus the tail window (the contract's tail
// mark), EOF, or the readiness deadline. Marks observed are never
// discarded mid-run: an absent mark stays absent and becomes an
// unavailable metric rather than a fabricated zero.
//
// The reader goroutine keeps draining to EOF after the main loop
// stops consuming, so a child can never stall on a full buffer.
func collectTimeline(src *os.File, timeout time.Duration, nbytes *atomic.Int64, c bench.Contract) (lines []markers.StampedLine, eof, timedOut bool, readErr error) {
	return collectTimelineHooked(src, timeout, nbytes, c, nil)
}

// collectTimelineHooked is collectTimeline with an optional callback
// invoked the instant a marker is observed, on the collector's
// goroutine, before any further reading.
//
// It exists so another suite can act at a marker without duplicating
// the pty, marker parsing, or teardown machinery: mem.tui.ready-rss
// samples process memory in this callback. The callback runs inline,
// so its own cost delays the next read; the measured lag is recorded
// as metadata rather than hidden.
func collectTimelineHooked(src *os.File, timeout time.Duration, nbytes *atomic.Int64, c bench.Contract, onMark func(ev string, at time.Time)) (lines []markers.StampedLine, eof, timedOut bool, readErr error) {
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
	proto := proto(c)
	have := map[string]bool{}
	// deadline bounds primary readiness; tail bounds the post-readiness
	// wait for the contract's tail mark so a subject that never emits it
	// cannot stretch every iteration by the full readiness timeout.
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var tailC <-chan time.Time
	complete := func() bool {
		if !have[c.TUI.PrimaryMark] {
			return false
		}
		return c.TUI.ReadinessTailMark == "" || have[c.TUI.ReadinessTailMark]
	}
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
			ev, isMark := proto.Event(l.Text)
			if isMark {
				have[ev] = true
				if onMark != nil {
					onMark(ev, l.At)
				}
			}
			if have[c.TUI.PrimaryMark] && tailC == nil {
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

// markMetric is the metric name for a mark's process-relative time.
func markMetric(ev string) string { return bench.MarkMetric(ev) }

// fillTimelineValues computes every owned metric from the timeline.
// A metric with a missing endpoint is recorded in unavailable (with the
// absent endpoint named) instead of being zero-filled: the runner keeps
// the raw vector aligned but drops those samples from that metric's
// statistics.
func fillTimelineValues(values map[string]float64, unavailable map[string]string, tl markers.Timeline, tSpawn time.Time, tui bench.TUI) {
	mark := func(ev string) {
		ms, ok := tl.Ms(ev, tSpawn)
		if !ok {
			unavailable[markMetric(ev)] = "marker " + ev + " not observed"
			return
		}
		values[markMetric(ev)] = ms
	}
	for _, ev := range tui.Marks {
		mark(ev)
	}
	seg := func(s bench.Segment) {
		from, oka := tSpawn, true
		if s.From != bench.SpawnAnchor {
			from, oka = tl.Marks[s.From]
		}
		to, okc := tl.Marks[s.To]
		switch {
		case !oka && !okc:
			unavailable[s.Name] = "endpoints " + s.From + " and " + s.To + " not observed"
		case !oka:
			unavailable[s.Name] = "endpoint " + s.From + " not observed"
		case !okc:
			unavailable[s.Name] = "endpoint " + s.To + " not observed"
		default:
			values[s.Name] = float64(to.Sub(from).Nanoseconds()) / 1e6
		}
	}
	for _, s := range tui.Segments {
		seg(s)
	}
}

// fillMissingTUI zeroes every owned metric for invalid samples so
// values arrays stay aligned with measure iterations. Invalid samples
// are excluded from statistics wholesale, so these zeros are
// placeholders, never reported data.
func fillMissingTUI(values map[string]float64, tui bench.TUI) {
	for _, ev := range tui.Marks {
		if _, ok := values[markMetric(ev)]; !ok {
			values[markMetric(ev)] = 0
		}
	}
	for _, s := range tui.Segments {
		if _, ok := values[s.Name]; !ok {
			values[s.Name] = 0
		}
	}
}
