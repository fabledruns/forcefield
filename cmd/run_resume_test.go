package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forcefield/internal/recovery"
	"forcefield/internal/runtime"
)

// isolateResumeGlobals saves the resume-related globals and restores
// them after the test, since cobra flag binding shares them process-wide.
func isolateResumeGlobals(t *testing.T) {
	t.Helper()
	origResumeID, origMaxTurns := resumeSessionID, resumeMaxTurns
	origAgent := agentFlag
	origRunner, origExit := resumeRunner, osExit
	t.Cleanup(func() {
		resumeSessionID, resumeMaxTurns = origResumeID, origMaxTurns
		agentFlag = origAgent
		resumeRunner, osExit = origRunner, origExit
	})
}

// isolateHome points config.Load at a temp dir so resume setup tests
// never touch a real ~/.forcefield.
func isolateResumeHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestRunArgs_ResumeTakesNoTask(t *testing.T) {
	isolateResumeGlobals(t)

	resumeSessionID = "some-id"
	if err := runCmd.Args(runCmd, []string{"task"}); err == nil {
		t.Error("run --resume with a task argument should fail Args validation")
	}
	if err := runCmd.Args(runCmd, []string{}); err != nil {
		t.Errorf("run --resume without args should pass Args validation: %v", err)
	}

	// Without --resume the established contract holds.
	resumeSessionID = ""
	if err := runCmd.Args(runCmd, []string{}); err == nil {
		t.Error("run without --resume or task should fail Args validation")
	}
	if err := runCmd.Args(runCmd, []string{"one"}); err != nil {
		t.Errorf("run with a task should pass Args validation: %v", err)
	}
}

func TestRunCommand_ResumeExitCodes(t *testing.T) {
	isolateResumeGlobals(t)

	cases := []struct {
		name string
		code int
		err  error
	}{
		{"completed", 0, nil},
		{"terminal", 2, errors.New("ff run --resume x stopped: nope")},
		{"retryable", 3, errors.New("ff run --resume x failed: blip")},
		{"needs human", 4, errors.New("ff run --resume x cancelled")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var exited *int
			resumeRunner = func(context.Context, string, int) (int, error) {
				return tc.code, tc.err
			}
			osExit = func(code int) {
				c := code
				exited = &c
			}
			resumeSessionID = "x"
			if err := runCommand([]string{}); err != nil {
				t.Fatalf("runCommand with stubbed runner error = %v", err)
			}
			if tc.code == 0 || tc.code == 1 {
				if exited != nil {
					t.Errorf("osExit(%d) called for code %d", *exited, tc.code)
				}
				return
			}
			if exited == nil {
				t.Fatalf("osExit not called for code %d", tc.code)
			}
			if *exited != tc.code {
				t.Errorf("osExit = %d, want %d", *exited, tc.code)
			}
		})
	}
}

func TestRunCommand_ResumeSetupErrorReturns(t *testing.T) {
	isolateResumeGlobals(t)
	isolateResumeHome(t)

	// "a/b" is not a valid session id: Load fails before touching disk.
	resumeSessionID = "a/b"
	calledExit := false
	osExit = func(int) { calledExit = true }
	if err := runCommand([]string{}); err == nil {
		t.Fatal("expected setup error for an invalid session id")
	}
	if calledExit {
		t.Error("osExit must not fire for setup failures (cobra owns exit 1)")
	}
}

func TestRunResumeSession_RejectsNegativeMaxTurns(t *testing.T) {
	isolateResumeGlobals(t)
	if code, err := runResumeSession(context.Background(), "whatever", -1); code != 1 || err == nil {
		t.Errorf("runResumeSession(-1) = (%d, %v), want (1, error)", code, err)
	}
}

func TestRunResumeSession_MissingSessionIsSetupError(t *testing.T) {
	isolateResumeGlobals(t)
	isolateResumeHome(t)

	// Valid id shape, absent file: read-only failure, no writes.
	code, err := runResumeSession(context.Background(), "does-not-exist", 0)
	if code != 1 || err == nil {
		t.Errorf("runResumeSession(missing) = (%d, %v), want (1, error)", code, err)
	}
}

func TestResumeOutcomeError(t *testing.T) {
	blocked := errors.New("stopped after 60 iterations (maximum reached)")
	newDriver := func() *recovery.Driver { return recovery.NewDriver(nil) }

	d := newDriver()
	d.HandleEvent(runtime.Event{Type: runtime.EventBlocked, Err: blocked})
	if err := resumeOutcomeError("s", d); err == nil ||
		!(strings.Contains(err.Error(), "s") && strings.Contains(err.Error(), "stopped")) {
		t.Errorf("blocked message = %v, want session id + reason", err)
	}

	d = newDriver()
	d.HandleEvent(runtime.Event{Type: runtime.EventCancelled})
	if err := resumeOutcomeError("s", d); err == nil ||
		!(strings.Contains(err.Error(), "s") && strings.Contains(err.Error(), "cancelled")) {
		t.Errorf("cancelled message = %v", err)
	}

	d = newDriver()
	d.HandleEvent(runtime.Event{Type: runtime.EventError, Err: errors.New("boom")})
	if err := resumeOutcomeError("s", d); err == nil ||
		!(strings.Contains(err.Error(), "s") && strings.Contains(err.Error(), "boom")) {
		t.Errorf("error message = %v", err)
	}

	d = newDriver()
	if err := resumeOutcomeError("s", d); err == nil {
		t.Errorf("no-outcome message = %v, want an error", err)
	}
}
