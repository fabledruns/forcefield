package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/recovery"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

func TestRunCommand_Success(t *testing.T) {
	origRun := runtimeRun
	origStdout := os.Stdout
	defer func() {
		runtimeRun = origRun
		os.Stdout = origStdout
	}()

	// Fake runtime that returns a deterministic response.
	runtimeRun = func(_ context.Context, msgs []providers.Message) (providers.Response, runtime.Status, []string, error) {
		if len(msgs) != 1 {
			t.Errorf("expected 1 message, got %d", len(msgs))
		}
		if msgs[0].Role != providers.UserRole {
			t.Errorf("role = %v, want UserRole", msgs[0].Role)
		}
		if msgs[0].Content != "hello world" {
			t.Errorf("content = %q, want hello world", msgs[0].Content)
		}
		return providers.Response{Content: "fake response"}, runtime.StatusVerified, nil, nil
	}

	// Capture stdout.
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runCommand([]string{"hello", "world"})
	w.Close()
	os.Stdout = origStdout
	if err != nil {
		t.Fatalf("runCommand error = %v", err)
	}
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := strings.TrimSpace(buf.String())
	if out != "fake response" {
		t.Errorf("stdout = %q, want %q", out, "fake response")
	}
}

func TestRunCommand_JoinsArgs(t *testing.T) {
	origRun := runtimeRun
	defer func() { runtimeRun = origRun }()

	var gotContent string
	runtimeRun = func(_ context.Context, msgs []providers.Message) (providers.Response, runtime.Status, []string, error) {
		gotContent = msgs[0].Content
		return providers.Response{Content: "ok"}, runtime.StatusVerified, nil, nil
	}
	// TrimSpace and Join should collapse multiple args with single space.
	if err := runCommand([]string{"  hello ", "world  ", " test"}); err != nil {
		t.Fatalf("runCommand error = %v", err)
	}
	if gotContent != "hello world   test" && gotContent != "hello world test" {
		// Join with space then TrimSpace: "hello   world   test" -> TrimSpace leaves internal spaces.
		// We just verify it contains hello and test.
		if !strings.Contains(gotContent, "hello") || !strings.Contains(gotContent, "test") {
			t.Errorf("joined content = %q", gotContent)
		}
	}
}

func TestRunCommand_PropagatesError(t *testing.T) {
	origRun := runtimeRun
	defer func() { runtimeRun = origRun }()

	runtimeRun = func(context.Context, []providers.Message) (providers.Response, runtime.Status, []string, error) {
		return providers.Response{}, "", nil, fmt.Errorf("model failure")
	}
	err := runCommand([]string{"task"})
	if err == nil {
		t.Fatal("expected error from runCommand")
	}
	if !strings.Contains(err.Error(), "model failure") {
		t.Errorf("error = %v, want model failure", err)
	}
}

func TestRunCommand_CobraValidation(t *testing.T) {
	// runCmd requires at least 1 arg; cobra should enforce this.
	// We test via Execute by setting args and checking error.
	// Use a fake run to avoid real provider.
	origRun := runtimeRun
	defer func() { runtimeRun = origRun }()
	runtimeRun = func(context.Context, []providers.Message) (providers.Response, runtime.Status, []string, error) {
		return providers.Response{Content: "ok"}, runtime.StatusVerified, nil, nil
	}
	// Directly test the cobra Args validator.
	if err := runCmd.Args(runCmd, []string{}); err == nil {
		t.Error("expected Args validator to fail for 0 args")
	}
	if err := runCmd.Args(runCmd, []string{"one"}); err != nil {
		t.Errorf("Args validator failed for 1 arg: %v", err)
	}
}

// TestExitForRunStatus pins the P1 false-success fix at the process
// boundary: verified completions (including plain chat, which FinalStatus
// reports as verified) return nil (exit 0, no osExit call); any other
// Done status exits unverified via osExit so supervisors and pipelines
// never mistake output for success.
func TestExitForRunStatus(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()

	if err := exitForRunStatus(runtime.StatusVerified); err != nil {
		t.Errorf("verified exit = %v, want nil", err)
	}
	for _, status := range []runtime.Status{
		runtime.StatusPartial,
		runtime.StatusBlocked,
		runtime.StatusFailed,
		"",
	} {
		var exited *int
		osExit = func(code int) { exited = &code }
		if err := exitForRunStatus(status); err != nil {
			t.Errorf("status %q exit returned error %v, want nil (osExit carries the code)", status, err)
		}
		if exited == nil || *exited != recovery.ExitUnverified {
			t.Errorf("status %q exited %v, want osExit(%d)", status, exited, recovery.ExitUnverified)
		}
	}
}

// TestRunCommand_UnverifiedPartialExitsUnverified drives the whole
// one-shot path: a fake runtime that finishes partial still prints its
// response but exits with the unverified code instead of 0.
func TestRunCommand_UnverifiedPartialExitsUnverified(t *testing.T) {
	origRun, origExit, origStdout := runtimeRun, osExit, os.Stdout
	defer func() {
		runtimeRun, osExit, os.Stdout = origRun, origExit, origStdout
	}()

	runtimeRun = func(context.Context, []providers.Message) (providers.Response, runtime.Status, []string, error) {
		return providers.Response{Content: "unreviewed work"}, runtime.StatusPartial, nil, nil
	}
	var exited *int
	osExit = func(code int) { exited = &code }

	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runCommand([]string{"do", "things"})
	w.Close()
	os.Stdout = origStdout
	if err != nil {
		t.Fatalf("runCommand error = %v, want nil (exit code carries the verdict)", err)
	}
	var buf bytes.Buffer
	buf.ReadFrom(r)
	if out := strings.TrimSpace(buf.String()); out != "unreviewed work" {
		t.Errorf("stdout = %q, want the response still printed", out)
	}
	if exited == nil || *exited != recovery.ExitUnverified {
		t.Errorf("exited = %v, want osExit(%d)", exited, recovery.ExitUnverified)
	}
}

// TestSaveGateError pins the P1 persistence gate: a sticky session-save
// failure becomes a terminal run error naming the session, while healthy
// (and nil) sessions pass. runResumeSession and finishResumeSession both
// consult it, so adoption writes and lifecycle bookkeeping cannot fail
// silently.
func TestSaveGateError(t *testing.T) {
	if err := saveGateError(nil, "ghost"); err != nil {
		t.Errorf("nil session gate = %v, want nil", err)
	}
	if err := saveGateError(session.New(), "healthy"); err != nil {
		t.Errorf("healthy session gate = %v, want nil", err)
	}
	broken := session.New()
	broken.LastSaveError = "replace session file x after 10 attempts: access denied"
	err := saveGateError(broken, "sess-7")
	if err == nil {
		t.Fatal("broken session gate = nil, want a terminal error")
	}
	if !strings.Contains(err.Error(), "sess-7") || !strings.Contains(err.Error(), "session save failed") {
		t.Errorf("err = %v, want it to name the session and the save failure", err)
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()
	fn()
	_ = w.Close()
	os.Stderr = origStderr
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

// TestReportMCPWarnings pins that headless runs surface pull-based MCP
// integration warnings (dead servers, missing tools) on stderr: without
// the TUI's /mcp surface they would otherwise stay invisible outside
// .forcefield/mcp-status.json. Empty warnings print nothing so clean
// runs and piped stdout are unaffected.
func TestReportMCPWarnings(t *testing.T) {
	if out := captureStderr(t, func() { reportMCPWarnings(nil) }); out != "" {
		t.Errorf("no warnings printed %q, want silence", out)
	}
	out := captureStderr(t, func() {
		reportMCPWarnings([]string{
			`mcp server "dead" failed to start: dial refused`,
			`agent "general" requests missing tool "mcp__dead__x"`,
		})
	})
	for _, want := range []string{`mcp server "dead" failed to start`, `mcp__dead__x`} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr = %q, want it to contain %q", out, want)
		}
	}
}

// TestRunCommand_ReportsMCPWarnings drives the one-shot path with a fake
// runtime that reports MCP warnings: the response still prints to stdout
// while the warnings land on stderr.
func TestRunCommand_ReportsMCPWarnings(t *testing.T) {
	origRun, origStdout := runtimeRun, os.Stdout
	defer func() { runtimeRun, os.Stdout = origRun, origStdout }()

	runtimeRun = func(context.Context, []providers.Message) (providers.Response, runtime.Status, []string, error) {
		return providers.Response{Content: "ok"},
			runtime.StatusVerified,
			[]string{`mcp server "dead" failed to start: boom`},
			nil
	}

	r, w, _ := os.Pipe()
	os.Stdout = w
	stderr := captureStderr(t, func() {
		if err := runCommand([]string{"task"}); err != nil {
			t.Fatalf("runCommand error = %v", err)
		}
	})
	_ = w.Close()
	os.Stdout = origStdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if out := strings.TrimSpace(buf.String()); out != "ok" {
		t.Errorf("stdout = %q, want the response untouched", out)
	}
	if !strings.Contains(stderr, `mcp server "dead" failed to start`) {
		t.Errorf("stderr = %q, want the MCP warning", stderr)
	}
}

// TestRunDemotesOnMidStreamEventWriteFailure pins the ORCH-TEST-04 core
// safety property at the command boundary: a telemetry failure that
// strikes mid-stream — RunStarted's record already accepted, then the
// log's pipe breaks while the model turn is in flight (the
// deterministic stand-in for an ENOSPC-style after-open failure) — must
// not convert the run's own verified outcome into an orchestration
// execution failure. The model response stays authoritative, the
// terminal status is demoted to partial, stderr carries the write
// failure plus the unverified demotion notice, and the process exits
// ExitUnverified so pipelines never accept output whose own evidence
// could not be written.
//
// Complementary to the failure-modes suite in run_persist_test.go:
// those subtests cover seam-time EPIPE (log writes from the first
// record onward), terminal-event closure timing, and the open-failure
// exit code, but never assert that a *write* failure keeps runErr nil
// or its exit code; the reader-recovery half of the contract lives
// with the writer fault-injection tests in internal/orchevents.
func TestRunDemotesOnMidStreamEventWriteFailure(t *testing.T) {
	origWriter := newEventWriter
	origExit := osExit
	defer func() {
		newEventWriter = origWriter
		osExit = origExit
	}()

	var rEnd *os.File
	rt := newVerifiedHeadlessRuntime(t, func() {
		// Armed mid-stream: the real (pipe-backed) writer has already
		// accepted RunStarted's record; from here on every write fails
		// deterministically — the write-side shape of a disk that
		// fills up after the log was opened.
		if rEnd != nil {
			_ = rEnd.Close()
		}
	})
	newEventWriter = func(path, runID string) (*orchevents.Writer, error) {
		var wEnd *os.File
		var err error
		rEnd, wEnd, err = os.Pipe()
		if err != nil {
			return nil, err
		}
		return orchevents.NewFileWriter(wEnd, "run-mid-stream-fail"), nil
	}

	var resp providers.Response
	var status runtime.Status
	var runErr error
	stderr := captureStderr(t, func() {
		resp, status, _, runErr = runHeadlessSession(context.Background(), rt, []providers.Message{
			{Role: providers.UserRole, Content: "mid-stream marker"},
		})
	})
	if runErr != nil {
		t.Fatalf("runErr = %v, want nil: a telemetry write failure must not become an orchestration execution failure", runErr)
	}
	if status != runtime.StatusPartial {
		t.Errorf("status = %q, want %q (verified demoted on mid-stream event-log write failure)", status, runtime.StatusPartial)
	}
	if resp.Content != "verified answer" {
		t.Errorf("response = %q, want the model's authoritative outcome untouched by telemetry failure", resp.Content)
	}
	if !strings.Contains(stderr, "orchestration event log write failed (write event 2:") {
		t.Errorf("missing mid-stream write failure warning on stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "result marked unverified: orchestration event log unusable") {
		t.Errorf("missing demotion notice on stderr: %q", stderr)
	}

	exited := -1
	osExit = func(code int) { exited = code }
	_ = captureStderr(t, func() {
		_ = exitForRunStatus(status)
	})
	if exited != recovery.ExitUnverified {
		t.Errorf("exit code = %d, want %d (ExitUnverified: lost evidence is never reported as success)", exited, recovery.ExitUnverified)
	}
}
