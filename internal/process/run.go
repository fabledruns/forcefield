package process

import (
	"context"
	"io"
	"os/exec"
	"time"
)

// terminateGracePeriod bounds how long Run waits for a child to exit
// after Terminate before escalating to Kill. It is a var so tests can
// shrink it; production keeps it generous because even a slow teardown
// is crash-safe (atomic saves, heal-on-adopt) while an orphaned tree
// is not.
var terminateGracePeriod = 5 * time.Second

// Run starts exe and supervises its whole process tree until it exits.
// Cancellation terminates cooperatively first, then kills after a grace
// period. See docs/Recovery.md. Stdio wiring is the caller's; nil ctx
// behaves like a live one.
func Run(ctx context.Context, exe string, args []string, stdout, stderr io.Writer, stdin io.Reader) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = stdin
	Configure(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	release := Track(cmd)
	defer release()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		return exitCode(err), err
	case <-ctx.Done():
	}

	// Cancellation path: ask first so a cooperative child (one that
	// traps the signal and tears down its own subtree, like `ff run`
	// cancelling its tools) exits cleanly and reports cancellation
	// instead of a kill.
	_ = Terminate(cmd)
	select {
	case err := <-waitDone:
		return exitCode(err), err
	case <-time.After(terminateGracePeriod):
	}

	_ = Kill(cmd)
	err := <-waitDone
	return exitCode(err), err
}

// exitCode extracts the process exit code from a Wait result: 0 on
// clean exit, the reported code on ExitError, -1 when the process never
// produced one (signaled, or a non-exit wait failure).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}
