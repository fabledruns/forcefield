package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"forcefield/internal/recovery"
)

// isolateSuperviseGlobals saves supervise flag globals and seams.
func isolateSuperviseGlobals(t *testing.T) {
	t.Helper()
	origRestarts, origBackoff, origMaxBackoff, origTurns :=
		superviseMaxRestarts, superviseBackoff, superviseMaxBackoff, superviseMaxTurns
	origSpawn, origExit := superviseSpawn, osExit
	t.Cleanup(func() {
		superviseMaxRestarts, superviseBackoff, superviseMaxBackoff, superviseMaxTurns =
			origRestarts, origBackoff, origMaxBackoff, origTurns
		superviseSpawn, osExit = origSpawn, origExit
	})
}

// TestHelperProcess is re-executed as a child process by the exec tests
// below (the standard library helper-process pattern, portable across
// Windows and Unix). It is never run as a real test.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("HELPER_MODE") {
	case "exit7":
		os.Exit(7)
	case "block":
		time.Sleep(time.Hour) // killed by the caller's context
	case "tree":
		spawnHeartbeatGrandchild()
		time.Sleep(time.Hour) // killed with the whole tree
	}
}

// spawnHeartbeatGrandchild starts a real detached descendant ticking
// HELPER_LOG, then returns: the caller blocks so both stay alive until
// the tree kill. Windows uses PowerShell (script file, no quoting
// layers); Unix uses sh. A start failure exits loudly (9) so the test
// fails visibly instead of asserting against a tree that never existed.
func spawnHeartbeatGrandchild() {
	log := os.Getenv("HELPER_LOG")
	if log == "" {
		fmt.Fprintln(os.Stderr, "tree helper needs HELPER_LOG")
		os.Exit(9)
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		script := "while($true){ Add-Content '" + log + "' 'tick'; Start-Sleep -Milliseconds 200 }\n"
		child := filepath.Join(filepath.Dir(log), "grandchild.ps1")
		if err := os.WriteFile(child, []byte(script), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "write grandchild script:", err)
			os.Exit(9)
		}
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-File", child)
	} else {
		cmd = exec.Command("sh", "-c", "( while true; do echo tick >> "+log+"; sleep 0.2; done & ) ; sleep 3600")
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start grandchild:", err)
		os.Exit(9)
	}
	// Deliberately never waited on: it must outlive this helper until
	// the tree kill reaps it.
}

func helperCommand(t *testing.T, mode string) (string, []string) {
	t.Helper()
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("HELPER_MODE", mode)
	return os.Args[0], []string{"-test.run=^TestHelperProcess$"}
}

func TestRunChildCommandExitCodes(t *testing.T) {
	exe, args := helperCommand(t, "exit0")
	if code, err := runChildCommand(context.Background(), exe, args, io.Discard, io.Discard, nil); code != 0 || err != nil {
		t.Errorf("exit0 helper = (%d, %v), want (0, nil)", code, err)
	}

	exe, args = helperCommand(t, "exit7")
	if code, err := runChildCommand(context.Background(), exe, args, io.Discard, io.Discard, nil); code != 7 || err != nil {
		t.Errorf("exit7 helper = (%d, %v), want (7, nil)", code, err)
	}
}

func TestRunChildCommandCancelledContext(t *testing.T) {
	exe, args := helperCommand(t, "block")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// The helper blocks until the tree lifecycle kills it; the
	// supervisor must read that as cancellation (4), not a retryable
	// failure.
	if code, err := runChildCommand(ctx, exe, args, io.Discard, io.Discard, nil); code != recovery.ExitNeedsHuman || err != nil {
		t.Errorf("killed helper = (%d, %v), want (4, nil)", code, err)
	}
}

func TestRunChildCommandCancelKillsDescendants(t *testing.T) {
	log := filepath.Join(t.TempDir(), "heartbeat.log")
	t.Setenv("HELPER_LOG", log)
	exe, args := helperCommand(t, "tree")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan [2]any, 1)
	go func() {
		code, err := runChildCommand(ctx, exe, args, io.Discard, io.Discard, nil)
		done <- [2]any{code, err}
	}()

	// Let the detached grandchild prove it is alive first.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if fi, err := os.Stat(log); err == nil && fi.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grandchild wrote no heartbeats; test setup failed")
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()

	select {
	case got := <-done:
		if got[0] != recovery.ExitNeedsHuman || got[1] != nil {
			t.Errorf("cancelled tree = (%v, %v), want (4, nil)", got[0], got[1])
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runChildCommand did not return after cancellation")
	}
	before, err := os.Stat(log)
	if err != nil {
		t.Fatalf("stat heartbeat: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	after, err := os.Stat(log)
	if err != nil {
		t.Fatalf("stat heartbeat: %v", err)
	}
	if after.Size() != before.Size() {
		t.Errorf("heartbeat grew after supervisor cancellation (%d -> %d bytes): descendant orphaned", before.Size(), after.Size())
	}
}

func TestRunChildCommandSpawnFailure(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "does-not-exist-ff")
	if _, err := runChildCommand(context.Background(), exe, nil, io.Discard, io.Discard, nil); err == nil {
		t.Error("missing binary should be a process failure, not an exit code")
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Errorf("spawn failure surfaced as ExitError %v; must stay a plain error", err)
		}
	}
}

func TestClassifyChildWait(t *testing.T) {
	if code, err := classifyChildWait(context.Background(), nil); code != 0 || err != nil {
		t.Errorf("nil err = (%d, %v), want (0, nil)", code, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if code, err := classifyChildWait(cancelled, errors.New("signal: killed")); code != 4 || err != nil {
		t.Errorf("cancelled ctx = (%d, %v), want (4, nil)", code, err)
	}
}

func TestChildArgs(t *testing.T) {
	got := childArgs("sess-1", 0)
	want := []string{"run", "--resume", "sess-1"}
	if len(got) != len(want) {
		t.Fatalf("childArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("childArgs = %v, want %v", got, want)
		}
	}

	got = childArgs("sess-1", 10)
	if len(got) != 5 || got[3] != "--max-turns" || got[4] != "10" {
		t.Errorf("childArgs with max-turns = %v, want --max-turns 10 appended", got)
	}

	// Session IDs pass through verbatim on every restart.
	for _, id := range []string{"a b", "x--y__z", "550e8400-e29b-41d4-a716-446655440000"} {
		got := childArgs(id, 0)
		if got[2] != id {
			t.Errorf("childArgs(%q)[2] = %q, want verbatim", id, got[2])
		}
	}
}

func TestValidateSuperviseFlags(t *testing.T) {
	if err := validateSuperviseFlags(5, time.Second, time.Minute, 0); err != nil {
		t.Errorf("sane flags rejected: %v", err)
	}
	for name, err := range map[string]error{
		"max-restarts": validateSuperviseFlags(-1, time.Second, time.Minute, 0),
		"backoff":      validateSuperviseFlags(1, -time.Second, time.Minute, 0),
		"max-backoff":  validateSuperviseFlags(1, time.Second, -time.Minute, 0),
		"max-turns":    validateSuperviseFlags(1, time.Second, time.Minute, -1),
	} {
		if err == nil {
			t.Errorf("negative %s accepted", name)
		}
	}
}

func TestSuperviseArgsValidation(t *testing.T) {
	if err := superviseCmd.Args(superviseCmd, []string{}); err == nil {
		t.Error("supervise without a session id should fail Args validation")
	}
	if err := superviseCmd.Args(superviseCmd, []string{"a", "b"}); err == nil {
		t.Error("supervise with two ids should fail Args validation")
	}
	if err := superviseCmd.Args(superviseCmd, []string{"sess-1"}); err != nil {
		t.Errorf("supervise with one id should pass Args validation: %v", err)
	}
}

// scriptSpawn replays canned child outcomes and records every attempt's
// (sessionID, maxTurns) so tests prove the ID is preserved.
func scriptSpawn(codes []int, calls *[][2]any) func(context.Context, string, int) (int, error) {
	n := 0
	return func(_ context.Context, sessionID string, maxTurns int) (int, error) {
		*calls = append(*calls, [2]any{sessionID, maxTurns})
		if n >= len(codes) {
			return 0, errors.New("child called past end of script")
		}
		code := codes[n]
		n++
		if code < 0 {
			return 0, errors.New("spawn failure")
		}
		return code, nil
	}
}

func superviseTestBudget() recovery.Budget {
	return recovery.Budget{MaxRestarts: 5, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
}

func TestSuperviseCommandRouting(t *testing.T) {
	isolateSuperviseGlobals(t)
	cases := []struct {
		name  string
		codes []int
		want  int
		calls int
	}{
		{"success stops", []int{0}, 0, 1},
		{"terminal stops", []int{2}, 2, 1},
		{"denied stops", []int{4}, 4, 1},
		{"retryable then success", []int{3, 0}, 0, 2},
		{"exhausted keeps last code", []int{3, 3, 3, 3, 3, 3, 3}, 3, 6},
		{"unknown code fails closed", []int{7}, 1, 1},
		{"spawn failure fails closed", []int{-1}, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][2]any
			superviseSpawn = scriptSpawn(tc.codes, &calls)
			if got := superviseCommand(context.Background(), "sess-9", 4, superviseTestBudget()); got != tc.want {
				t.Errorf("superviseCommand = %d, want %d", got, tc.want)
			}
			if len(calls) != tc.calls {
				t.Fatalf("attempts = %d, want %d", len(calls), tc.calls)
			}
			// Every restart replays the same session with the same bound.
			for _, c := range calls {
				if c[0] != "sess-9" || c[1] != 4 {
					t.Errorf("attempt args = %v, want [sess-9 4] on every restart", c)
				}
			}
		})
	}
}

func TestSuperviseCommandTouchesNoSessionState(t *testing.T) {
	isolateSuperviseGlobals(t)
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatal(err)
		}
	})

	var calls [][2]any
	superviseSpawn = scriptSpawn([]int{0}, &calls)
	if got := superviseCommand(context.Background(), "ghost-session", 0, superviseTestBudget()); got != 0 {
		t.Fatalf("superviseCommand = %d, want 0", got)
	}
	// The supervisor never opens the session itself: no state dir may
	// appear as its side effect.
	if _, err := os.Stat(filepath.Join(dir, ".forcefield")); !os.IsNotExist(err) {
		t.Errorf(".forcefield state appeared under supervisor cwd: %v", err)
	}
}

func TestSuperviseRunEExitPropagation(t *testing.T) {
	isolateSuperviseGlobals(t)
	superviseMaxRestarts, superviseBackoff, superviseMaxBackoff, superviseMaxTurns = 1, time.Millisecond, time.Millisecond, 0

	superviseSpawn = func(context.Context, string, int) (int, error) { return 3, nil }
	var exited *int
	osExit = func(code int) {
		c := code
		exited = &c
	}
	// RunE wires flags/globals into superviseCommand; stub the child and
	// observe the exit code without exiting the test process.
	runE := superviseCmd.RunE
	if runE == nil {
		t.Fatal("superviseCmd RunE is nil")
	}
	if err := runE(superviseCmd, []string{"sess-1"}); err != nil {
		t.Fatalf("RunE error = %v", err)
	}
	if exited == nil || *exited != 3 {
		t.Errorf("osExit = %v, want 3", exited)
	}
}

func TestSuperviseRunERejectsBadFlags(t *testing.T) {
	isolateSuperviseGlobals(t)
	superviseMaxRestarts = -1
	called := false
	superviseSpawn = func(context.Context, string, int) (int, error) {
		called = true
		return 0, nil
	}
	if err := superviseCmd.RunE(superviseCmd, []string{"sess-1"}); err == nil {
		t.Fatal("expected flag validation error")
	}
	if called {
		t.Error("child must not spawn when flags are invalid")
	}
}
