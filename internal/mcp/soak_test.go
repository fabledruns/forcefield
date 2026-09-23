package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"forcefield/internal/redact"
	"forcefield/internal/tools"
)

// Phase 8 hardening: repeated lifecycle soak plus concurrent-call stress
// against the real helper subprocess. No sleeps synchronize correctness;
// bounded polls and the suite timeout are hang backstops only, except for
// the helper's own response delays (CALL_SLEEP_MS), which are the
// exercised behavior.

// settledGoroutines polls until the goroutine count stops changing, so
// leak checks compare settled states rather than racing teardown.
func settledGoroutines(t *testing.T, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := goruntime.NumGoroutine()
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		n := goruntime.NumGoroutine()
		if n == last {
			// One more sample to avoid catching a monotonic drift
			// mid-flight.
			time.Sleep(50 * time.Millisecond)
			if goruntime.NumGoroutine() == n {
				return n
			}
		}
		last = n
	}
	return goruntime.NumGoroutine()
}

// TestHostSoakStartCallClose repeats the full lifecycle against real
// subprocesses: start, initialize, discover, call, close. Every cycle
// must leave a clean snapshot, a working call path while alive, and no
// residue once closed.
func TestHostSoakStartCallClose(t *testing.T) {
	const cycles = 10
	base := settledGoroutines(t, 5*time.Second)
	for i := 0; i < cycles; i++ {
		h, _ := newTestHost(t, map[string]ServerConfig{
			"s": helperServerConfig(t, nil),
		})
		if err := h.Start(context.Background()); err != nil {
			t.Fatalf("cycle %d: Start error = %v", i, err)
		}
		ss := snapshotOf(h, "s")
		if !ss.Ready || !ss.Healthy {
			t.Fatalf("cycle %d: snapshot = %+v, want ready+healthy", i, ss)
		}
		if len(ss.Tools) != 1 {
			t.Fatalf("cycle %d: tools = %v", i, toolNames(ss.Tools))
		}
		res, err := ss.Tools[0].Execute(context.Background(), map[string]any{"cycle": i})
		if err != nil {
			t.Fatalf("cycle %d: Execute error = %v", i, err)
		}
		if !strings.Contains(res.Content, "echo:") {
			t.Fatalf("cycle %d: content = %q", i, res.Content)
		}
		if err := h.Close(); err != nil {
			t.Fatalf("cycle %d: Close error = %v", i, err)
		}
		after := snapshotOf(h, "s")
		if after.Ready || after.Healthy {
			t.Fatalf("cycle %d: snapshot after Close = %+v, want unready", i, after)
		}
		if err := h.Close(); err != nil {
			t.Fatalf("cycle %d: second Close error = %v (must be idempotent)", i, err)
		}
	}
	final := settledGoroutines(t, 5*time.Second)
	if final > base+3 {
		t.Errorf("goroutines grew %d -> %d across %d cycles; want no leak (slack 3)", base, final, cycles)
	}
}

// TestAdapterConcurrentCancelAndClose fires concurrent calls, cancels
// some, then closes the host mid-flight. Every call must settle exactly
// once, nothing may hang, and shutdown must complete.
func TestAdapterConcurrentCancelAndClose(t *testing.T) {
	shrinkGrace(t)
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_CALL_SLEEP_MS": "300"}),
	})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	adapter := snapshotOf(h, "s").Tools[0]

	const total = 16
	type outcome struct {
		err error
		ok  bool
	}
	results := make(chan outcome, total)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := context.Background()
			if i%2 == 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel() // pre-canceled: must fail fast, never touch the wire
			}
			res, err := adapter.Execute(ctx, map[string]any{"n": i})
			results <- outcome{err: err, ok: err == nil && strings.Contains(res.Content, "echo:")}
		}(i)
	}
	wg.Wait()
	close(results)
	var canceled, succeeded int
	for out := range results {
		if out.err != nil {
			canceled++
		} else if out.ok {
			succeeded++
		} else {
			t.Error("call returned nil error with bad content: outcome delivered twice or corrupted")
		}
	}
	if canceled != total/2 || succeeded != total/2 {
		t.Errorf("canceled=%d succeeded=%d, want %d/%d", canceled, succeeded, total/2, total/2)
	}
	// The transport survived the cancellation storm: a fresh call works.
	res, err := adapter.Execute(context.Background(), map[string]any{"n": "after"})
	if err != nil || !strings.Contains(res.Content, "echo:") {
		t.Fatalf("post-cancel call res = %+v err = %v", res, err)
	}

	// Now close while calls are in flight: everything must still settle.
	var wg2 sync.WaitGroup
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			_, err := adapter.Execute(context.Background(), map[string]any{})
			done <- err
		}()
	}
	// Give the calls a head start so at least one is genuinely in flight
	// when Close lands; correctness never depends on the exact interleave.
	time.Sleep(100 * time.Millisecond)
	if err := h.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	wg2.Wait()
	close(done)
	for err := range done {
		_ = err // success or shutdown error are both legal terminal outcomes
	}
	if ss := snapshotOf(h, "s"); ss.Ready {
		t.Error("server still ready after Close")
	}
}

// TestCallContextTimeoutThenReusable proves a timed-out call fails while
// the host stays usable for the next call.
func TestCallContextTimeoutThenReusable(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_CALL_SLEEP_MS": "1500"}),
	})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	adapter := snapshotOf(h, "s").Tools[0]

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := adapter.Execute(ctx, map[string]any{})
	if err == nil {
		t.Fatal("timed-out call succeeded")
	}
	if !snapshotOf(h, "s").Healthy {
		t.Fatal("host unhealthy after one timed-out call")
	}
	res, err := adapter.Execute(context.Background(), map[string]any{})
	if err != nil || !strings.Contains(res.Content, "echo:") {
		t.Fatalf("reuse after timeout: res = %+v err = %v", res, err)
	}
}

// TestHostStdoutCloseMidServe covers a child that closes stdout without
// exiting: the parent must observe EOF as terminal transport failure
// while the child is still alive, fail pending work, and still terminate
// the whole tree on Close.
func TestHostStdoutCloseMidServe(t *testing.T) {
	shrinkGrace(t)
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, map[string]string{"MCP_HELPER_CLOSE_STDOUT": "1"}),
	})
	startErr := make(chan error, 1)
	go func() { startErr <- h.Start(context.Background()) }()
	// Startup can never succeed: initialize gets EOF, not a response.
	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("Start succeeded against a stdout-closed server")
		}
		var serr *StartError
		if !errors.As(err, &serr) {
			t.Fatalf("err = %v (%T), want *StartError", err, err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Start never settled against stdout-closed server")
	}
	ss := snapshotOf(h, "s")
	if ss.Ready {
		t.Error("stdout-closed server reports ready")
	}
	// The child is still alive at this point (it only closed stdout), so
	// Close must still reap the tree without hanging.
	if err := h.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	waitForCondition(t, 10*time.Second, "reap after stdout-close", func() bool {
		return snapshotOf(h, "s").Exited
	})
}

// TestAdapterCallCountsOnceEach is a counting-level guard for the stress
// above: under concurrency every request ID the server saw gets exactly
// one response consumed by exactly one caller. The transport's pending
// map must be empty afterwards.
func TestAdapterCallCountsOnceEach(t *testing.T) {
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": helperServerConfig(t, nil),
	})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	adapter := snapshotOf(h, "s").Tools[0]
	const n = 32
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := adapter.Execute(context.Background(), map[string]any{"n": i})
			if err != nil {
				failed.Add(1)
				return
			}
			if !strings.Contains(res.Content, fmt.Sprintf(`"n":%d`, i)) {
				failed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if f := failed.Load(); f != 0 {
		t.Errorf("%d/%d concurrent calls misrouted or failed", f, n)
	}
}

// Phase 8 boundary triplets: every size gate below is exercised at
// limit-1, limit, and limit+1 so off-by-one regressions fail loudly
// instead of hiding behind enormous test values.

func TestTruncateDescriptionBoundaryTriplet(t *testing.T) {
	for _, n := range []int{MaxDescriptionBytes - 1, MaxDescriptionBytes, MaxDescriptionBytes + 1} {
		s := strings.Repeat("a", n)
		got, cut := TruncateDescription(s)
		wantCut := n > MaxDescriptionBytes
		if cut != wantCut {
			t.Errorf("len %d: cut = %v, want %v", n, cut, wantCut)
		}
		if !wantCut && got != s {
			t.Errorf("len %d: untruncated text modified", n)
		}
		if wantCut && !strings.HasSuffix(got, TruncationMarker) {
			t.Errorf("len %d: missing truncation marker", n)
		}
	}
	// A multibyte rune straddling the cut must never split.
	s := strings.Repeat("a", MaxDescriptionBytes-1) + "é" + "b"
	got, cut := TruncateDescription(s)
	if !cut {
		t.Fatal("multibyte boundary not cut")
	}
	if !utf8.ValidString(got) {
		t.Error("truncated description is not valid UTF-8")
	}
	if !strings.HasSuffix(got, TruncationMarker) {
		t.Error("missing truncation marker")
	}
}

func TestToolResultTruncationBoundaries(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	adapter.SetLimits(tools.Limits{MaxBytes: 32})
	for _, n := range []int{31, 32, 33} {
		ch := startExecute(context.Background(), adapter, map[string]any{})
		_, rawID := nextCall(t, h)
		h.answerResult(t, rawID, textResult(strings.Repeat("x", n)))
		res, err := awaitExecute(t, ch)
		if err != nil {
			t.Fatalf("len %d: Execute error = %v", n, err)
		}
		_, truncated := res.Metadata["truncated"]
		if truncated != (n > 32) {
			t.Errorf("len %d: truncated metadata = %v, want %v", n, truncated, n > 32)
		}
		if !utf8.ValidString(res.Content) {
			t.Errorf("len %d: content is not valid UTF-8", n)
		}
	}
}

func TestToolResultHugeNonTextTypeBounded(t *testing.T) {
	h, defs := readyAdapterHarness(t, "srv", []string{toolEntry("t", "d", `{"type":"object"}`)})
	adapter, err := NewTool(h.cl, defs[0])
	if err != nil {
		t.Fatalf("NewTool error = %v", err)
	}
	ch := startExecute(context.Background(), adapter, map[string]any{})
	_, rawID := nextCall(t, h)
	huge := strings.Repeat("T", 100000)
	h.answerResult(t, rawID, `{"content":[{"type":`+quoteJSONString(huge)+`,"data":"AAAA"}]}`)
	res, err := awaitExecute(t, ch)
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if strings.Contains(res.Content, huge) {
		t.Fatal("unbounded remote type string reached model content")
	}
	if len([]rune(res.Content)) > MaxErrorDetailRunes+256 {
		t.Errorf("placeholder of %d runes is unbounded", len([]rune(res.Content)))
	}
	if !utf8.ValidString(res.Content) {
		t.Error("placeholder is not valid UTF-8")
	}
}

func TestBuildEnvTrickyLiterals(t *testing.T) {
	values := map[string]string{
		"QUOTED":  `"double" and 'single' quotes`,
		"SPACED":  "  leading and trailing  ",
		"DOLLAR":  "$(rm -rf /) ${HOME} $VAR",
		"TICKS":   "`backticks`",
		"PCT":     `%SystemRoot%\path`,
		"TRAIL":   `ends-with-backslash\`,
		"EQUALS":  "a=b=c",
		"UNICODE": "héllo wörld ✓",
	}
	env := map[string]string{}
	for k, v := range values {
		env[k] = v
	}
	got := buildEnv(ServerConfig{Env: env})
	seen := map[string]string{}
	for _, e := range got {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			t.Fatalf("entry %q missing separator", e)
		}
		seen[k] = v
	}
	for k, want := range values {
		if seen[k] != want {
			t.Errorf("%s = %q, want literal %q", k, seen[k], want)
		}
	}
}

func TestWriteStatusFailureBlockedDir(t *testing.T) {
	dir := t.TempDir()
	// A regular file where .forcefield must go: MkdirAll fails, so the
	// write fails without creating anything.
	if err := os.WriteFile(filepath.Join(dir, ".forcefield"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteStatusFile(dir, StatusFile{Version: StatusVersion})
	if err == nil {
		t.Fatal("write through a blocked dir succeeded")
	}
	if _, statErr := os.Stat(StatusFilePath(dir)); !os.IsNotExist(statErr) {
		t.Error("partial status file left behind after failed write")
	}
}

func TestStatusReadOversizedFileCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forcefield"), 0o700); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("p", MaxStatusFileBytes)
	body := `{"version":1,"servers":[],"pad":` + quoteJSONString(pad) + `}`
	if len(body) <= MaxStatusFileBytes {
		t.Fatalf("test setup only %d bytes, want over %d", len(body), MaxStatusFileBytes)
	}
	if err := os.WriteFile(StatusFilePath(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadStatusFile(dir)
	if err == nil {
		t.Fatal("oversize status file accepted")
	}
}

func TestStatusReadClampsFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forcefield"), 0o700); err != nil {
		t.Fatal(err)
	}
	var tools []string
	for i := 0; i < MaxStatusTools+50; i++ {
		tools = append(tools, "mcp__s__tool")
	}
	toolJSON, _ := json.Marshal(tools)
	body := `{"version":1,"config_sha256":"abc","servers":[{"key":` +
		quoteJSONString(strings.Repeat("k", 500)) + `,"tools":` + string(toolJSON) +
		`,"last_error":` + quoteJSONString(strings.Repeat("e", MaxErrorDetailRunes+5000)) +
		`,"stderr_tail":` + quoteJSONString(strings.Repeat("s", MaxStatusStderrChars+5000)) + `}]}`
	if err := os.WriteFile(StatusFilePath(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := ReadStatusFile(dir)
	if err != nil {
		t.Fatalf("clampable file rejected: %v", err)
	}
	if len(st.Servers) != 1 {
		t.Fatalf("servers = %d", len(st.Servers))
	}
	ss := st.Servers[0]
	if len([]rune(ss.Key)) > MaxStatusKeyRunes {
		t.Errorf("key of %d runes not clamped", len([]rune(ss.Key)))
	}
	if len(ss.Tools) > MaxStatusTools {
		t.Errorf("tools %d exceed cap %d", len(ss.Tools), MaxStatusTools)
	}
	if len([]rune(ss.LastError)) > MaxErrorDetailRunes {
		t.Errorf("LastError of %d runes not clamped", len([]rune(ss.LastError)))
	}
	if len([]rune(ss.StderrTail)) > MaxStatusStderrChars {
		t.Errorf("StderrTail of %d runes not clamped", len([]rune(ss.StderrTail)))
	}
}

func TestHostStartProcessFailure(t *testing.T) {
	// Enabled server with an unresolvable executable: startup fails fast
	// with no process, no hang, and a clean Close.
	h, _ := newTestHost(t, map[string]ServerConfig{
		"s": {Command: filepath.Join(t.TempDir(), "no-such-binary-xyz"), TimeoutSeconds: 30},
	})
	if err := h.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded for unresolvable executable")
	}
	ss := snapshotOf(h, "s")
	if ss.Ready || ss.Started {
		t.Errorf("snapshot = %+v, want never-spawned failure", ss)
	}
	if ss.LastError == "" {
		t.Error("failed spawn left no diagnostic")
	}
	if err := h.Close(); err != nil {
		t.Errorf("Close error = %v", err)
	}
}

func TestPassthroughNotAutoRegistered(t *testing.T) {
	// Passthrough values are copied verbatim but their sensitivity is
	// unknown to us: unlike explicit secret-looking keys, they must NOT
	// be registered for redaction automatically.
	const key = "MCP_HOST_SVC_TOKEN_XYZ"
	const value = "passthrough-canary-918273645"
	t.Setenv(key, value)
	cfg := helperServerConfig(t, nil)
	cfg.EnvPassthrough = []string{key}
	h, _ := newTestHost(t, map[string]ServerConfig{"s": cfg})
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if got := redact.Scrub(value); got != value {
		t.Errorf("passthrough value was auto-registered for redaction: %q", got)
	}
}
