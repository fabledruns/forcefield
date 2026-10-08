package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"forcefield/internal/process"
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// waitDelay bounds how long cmd.Wait() will wait for stdout/stderr pipes
// to drain after the process group has been killed (context cancelled or
// timed out). Without it, a killed process whose children keep a pipe fd
// open (e.g. a backgrounded grandchild) could make Wait block forever
// even though the command itself is long dead.
const waitDelay = time.Second

// Execute runs the command to completion without streaming intermediate
// output. It's a thin wrapper around ExecuteStream with a no-op sink, kept
// so Shell satisfies the plain tools.Tool interface for callers that don't
// care about live output.
func (s *Shell) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	return s.ExecuteStream(ctx, args, nil)
}

// ExecuteStream runs command with per-line sanitized streaming. Never writes
// to process stdio; results flow as Result/chunks. See docs/Tools.md.
func (s *Shell) ExecuteStream(ctx context.Context, args map[string]any, onChunk func(tools.StreamChunk)) (tools.Result, error) {
	command, err := tools.StringArg(args, "command")
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(command) == "" {
		return tools.Result{}, &tools.ArgumentError{Field: "command", Reason: "must not be empty"}
	}

	// No TTY here (stdin /dev/null, piped output): refuse interactive
	// programs up front. See docs/Tools.md.
	if prog, ok := detectInteractiveCommand(command); ok {
		return tools.Result{
			IsError: true,
			Content: fmt.Sprintf(
				"refusing to run %q: it requires an interactive terminal (tty), which the shell tool does not provide",
				prog,
			),
			Tool:    "shell",
			Command: command,
		}, nil
	}

	// WSL lexical mitigation only, not a boundary (see docs/Sandbox.md).
	if s.isWSLMode(ctx) && isWSLForbiddenPattern(command) {
		return tools.Result{
			IsError: true,
			Content: fmt.Sprintf(
				"refusing to run %q: WSL shell access to host filesystem (%s) is blocked as a conservative mitigation (not a sandbox); use workspace-relative paths instead",
				command, wslForbiddenSnippet(command),
			),
			Tool:    "shell",
			Command: command,
		}, nil
	}

	cwd, err := tools.OptionalStringArg(args, "cwd", "")
	if err != nil {
		return tools.Result{}, err
	}
	// Directory existence and workspace-scope validation happen inside the
	// executor (Prepare), which is the only place policy lives. Failures
	// come back as typed errors mapped to Results below, so the model sees
	// a clear, retryable message instead of an opaque execution failure.

	// A bad timeout_seconds must be an argument error, not silently ignored:
	// falling back to the default would kill a long command the caller
	// asked to run longer, which looks exactly like "the command never ran".
	bounds := s.resolveLimits()
	timeout := bounds.Timeout
	if raw, ok := args["timeout_seconds"]; ok {
		secs, ok := toFloat(raw)
		if !ok || secs <= 0 {
			return tools.Result{}, &tools.ArgumentError{Field: "timeout_seconds", Reason: "must be a positive number of seconds"}
		}
		if secs > float64(tools.MaxTimeout/time.Second) {
			return tools.Result{}, &tools.ArgumentError{Field: "timeout_seconds", Reason: fmt.Sprintf("must be at most %d seconds", int(tools.MaxTimeout/time.Second))}
		}
		timeout = time.Duration(secs * float64(time.Second))
	}
	timeout = tools.ClampTimeout(timeout, bounds.Timeout)

	envPairs, err := extraEnvArgs(args)
	if err != nil {
		return tools.Result{}, err
	}

	// Probe the execution backend once per Shell (WSL availability on
	// Windows, Bash on Unix) before spawning anything, using the outer
	// context so a cold WSL boot doesn't consume the command's own timeout.
	if err := s.ensureBackend(ctx); err != nil {
		return tools.Result{
			IsError:  true,
			Content:  err.Error(),
			ExitCode: -1,
			Tool:     "shell",
			Command:  command,
		}, nil
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	prepared, err := s.executorFor().Prepare(runCtx, sandbox.Request{
		Command:  command,
		Dir:      cwd,
		ExtraEnv: envPairs,
	})
	switch {
	case err == nil:
	case errors.Is(err, sandbox.ErrInvalidDir):
		return tools.Result{
			IsError: true,
			Content: fmt.Sprintf("working directory does not exist: %s", cwd),
			Tool:    "shell",
			Command: command,
		}, nil
	case errors.Is(err, sandbox.ErrWorkspaceEscape):
		return tools.Result{
			IsError: true,
			Content: fmt.Sprintf("working directory %q is outside the allowed workspace; Forcefield will not widen its scope", cwd),
			Tool:    "shell",
			Command: command,
		}, nil
	default:
		return tools.Result{
			IsError:  true,
			Content:  err.Error(),
			ExitCode: -1,
			Tool:     "shell",
			Command:  command,
		}, nil
	}
	cmd, cleanup := prepared.Cmd, prepared.Cleanup
	if cleanup != nil {
		defer cleanup()
	}

	// Isolate the child from the real terminal (stdin EOF; piped output).
	cmd.Stdin = nil

	// Kill the whole subtree on cancel/timeout (see internal/process).
	process.Configure(cmd)
	cmd.Cancel = func() error { return process.Kill(cmd) }
	cmd.WaitDelay = waitDelay

	// Own the pipes instead of using StdoutPipe/StderrPipe. The latter are
	// closed by cmd.Wait, so waiting for the readers before calling Wait can
	// deadlock when a descendant keeps an inherited pipe handle open. With
	// explicit pipes, Wait can run concurrently and cancellation can close
	// the readers to unblock the stream goroutines immediately.
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return tools.Result{}, fmt.Errorf("shell: create stdout pipe: %w", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return tools.Result{}, fmt.Errorf("shell: create stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	started := time.Now()
	if err := cmd.Start(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		// Start failures (bad cwd, missing interpreter, permission denied)
		// are reported as Results like every other command failure, so the
		// model always gets stdout/stderr/exit-code-shaped feedback.
		return tools.Result{
			IsError:    true,
			Content:    fmt.Sprintf("failed to start command: %v", err),
			ExitCode:   -1,
			Tool:       "shell",
			Command:    command,
			DurationMs: time.Since(started).Milliseconds(),
		}, nil
	}
	// The child owns the duplicated write handles after Start. Keeping the
	// parent's copies open would prevent EOF when the command exits.
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	// Track the tree for the command's lifetime: the job backstop reaps
	// anything the synchronous Kill misses and anything that would
	// otherwise outlive us.
	release := process.Track(cmd)
	defer release()

	output := &shellOutput{max: bounds.MaxBytes}
	pipeDone := make(chan struct{}, 2)
	pipesFinished := make(chan struct{})
	go func() {
		<-pipeDone
		<-pipeDone
		close(pipesFinished)
	}()

	go streamPipe(stdoutReader, "stdout", output, onChunk, pipeDone)
	go streamPipe(stderrReader, "stderr", output, onChunk, pipeDone)

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	// Wait for the process first. On cancellation, CommandContext invokes
	// cmd.Cancel and this returns after the process is killed. On normal
	// completion, this also avoids making process completion depend on a
	// descendant that inherited stdout/stderr.
	waitErr := <-waitDone

	// Let readers drain naturally, but never wait forever for a descendant
	// that retained an inherited pipe handle. Closing the read ends also
	// unblocks streamPipe, allowing the result to return after cancellation
	// or after a short bounded drain window on normal completion.
	select {
	case <-pipesFinished:
	case <-time.After(waitDelay):
		_ = stdoutReader.Close()
		_ = stderrReader.Close()
		<-pipesFinished
	}
	_ = stdoutReader.Close()
	_ = stderrReader.Close()
	duration := time.Since(started)

	exitCode := 0
	success := true
	if waitErr != nil {
		success = false
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	stdoutStr := output.stdoutString()
	stderrStr := output.stderrString()
	truncated := output.isTruncated()
	// Structured truncation record for Result.Metadata, plus the
	// long-standing model-visible marker (wording preserved).
	var truncMeta map[string]any
	truncNote := ""
	if truncated {
		kept, dropped, max := output.totalBytes(), output.droppedBytes(), bounds.MaxBytes
		truncMeta = tools.Truncation{Truncated: true, OriginalBytes: kept + dropped, KeptBytes: kept, Limit: max}.Fields()
		truncNote = fmt.Sprintf("\n[...output truncated at %d bytes (limit %d bytes), further output discarded]", max, max)
	}

	if runCtx.Err() == context.DeadlineExceeded {
		content := fmt.Sprintf("command timed out after %s", timeout)
		if truncated {
			content += truncNote
		}
		return tools.Result{
			IsError:    true,
			Content:    content,
			ExitCode:   -1,
			Stdout:     stdoutStr,
			Stderr:     stderrStr,
			DurationMs: duration.Milliseconds(),
			Tool:       "shell",
			Command:    command,
			Metadata:   truncMeta,
		}, nil
	}
	if runCtx.Err() == context.Canceled {
		content := "command was cancelled"
		if truncated {
			content += truncNote
		}
		return tools.Result{
			IsError:    true,
			Content:    content,
			ExitCode:   -1,
			Stdout:     stdoutStr,
			Stderr:     stderrStr,
			DurationMs: duration.Milliseconds(),
			Tool:       "shell",
			Command:    command,
			Metadata:   truncMeta,
		}, nil
	}

	// Distinguish backend infrastructure failures (e.g. WSL's distribution
	// failing to start or mount mid-session) from the command's own errors,
	// so the model learns its command never properly ran instead of seeing
	// an unexplained non-zero exit from Bash.
	if !success {
		if detail, hint := sandbox.BackendFailure(stderrStr, stdoutStr); hint != "" {
			return tools.Result{
				IsError:    true,
				Content:    fmt.Sprintf("shell backend error: WSL could not run the command: %s\n%s", detail, hint),
				ExitCode:   exitCode,
				Stdout:     stdoutStr,
				Stderr:     stderrStr,
				DurationMs: duration.Milliseconds(),
				Tool:       "shell",
				Command:    command,
				Metadata:   truncMeta,
			}, nil
		}
	}

	content := stdoutStr
	hasStderr := len(stderrStr) > 0
	if hasStderr {
		// Keep stderr visible even on success: callers reading only Content
		// would otherwise lose diagnostics the command wrote to stderr.
		if content == "" {
			content = fmt.Sprintf("stderr:\n%s", stderrStr)
		} else {
			content = fmt.Sprintf("%s\nstderr:\n%s", content, stderrStr)
		}
	}
	if !success {
		content = fmt.Sprintf("command exited with code %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdoutStr, stderrStr)
	}
	if truncated {
		content += truncNote
	}

	return tools.Result{
		Content:    content,
		IsError:    !success,
		ExitCode:   exitCode,
		Stdout:     stdoutStr,
		Stderr:     stderrStr,
		DurationMs: duration.Milliseconds(),
		Tool:       "shell",
		Command:    command,
		Metadata:   truncMeta,
	}, nil
}
