package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// defaultTimeout bounds how long a shell command may run when the caller
// doesn't specify one via the "timeout" argument.
const defaultTimeout = 30 * time.Second

// waitDelay bounds how long cmd.Wait() will wait for stdout/stderr pipes
// to drain after the process group has been killed (context cancelled or
// timed out). Without it, a killed process whose children keep a pipe fd
// open (e.g. a backgrounded grandchild) could make Wait block forever
// even though the command itself is long dead.
const waitDelay = time.Second

// Shell executes shell commands inside the current project directory (or
// a caller-specified working directory), capturing stdout and stderr
// separately and streaming output live as the command runs.
//
// Terminal ownership: Shell never touches os.Stdout/os.Stderr/os.Stdin or
// prints anything itself. Bubble Tea is the only thing allowed to write to
// the real terminal (see internal/tui). Everything a command produces is
// captured through pipes, sanitized, and handed to the caller as plain
// Result/StreamChunk data that flows through the runtime's event system;
// the TUI decides how (and whether) to render it.
//
// Process construction is delegated entirely to a sandbox.Executor: this
// tool requests execution, the executor enforces policy (working
// directory scope, environment forwarding rules, backend selection), and
// nothing here can bypass it because no other command-building path
// exists. Shell owns only argument validation, output plumbing,
// sanitization, and process teardown.
type Shell struct {
	// executor builds validated commands. Nil means native mode with the
	// historical unrestricted behavior; runtime.New wires the configured
	// sandbox executor in.
	executor sandbox.Executor
	// executorOnce guards one-time construction of the default executor.
	executorOnce sync.Once

	// backendMu guards the one-time backend probe below.
	backendMu sync.Mutex
	// backendErr caches the result of probing the Bash execution backend
	// (WSL availability on Windows; see ensureBackend). A nil backendErr
	// with backendProbed set means the backend is healthy.
	backendErr    error
	backendProbed bool
}

// NewShell returns a ready-to-register Shell tool using native execution.
func NewShell() *Shell { return &Shell{} }

// NewShellWithExecutor returns a Shell tool whose commands are built by
// the given sandbox.Executor. The executor is authoritative: when it
// refuses (unavailable WSL, workspace escape, impossible network
// isolation), the tool reports an error instead of falling back.
func NewShellWithExecutor(e sandbox.Executor) *Shell { return &Shell{executor: e} }

// executorFor lazily supplies the default executor so zero-value Shells
// keep working for tests and simple callers.
func (s *Shell) executorFor() sandbox.Executor {
	if s.executor != nil {
		return s.executor
	}
	s.executorOnce.Do(func() {
		e, err := sandbox.NewExecutor(sandbox.DefaultPolicy())
		if err != nil {
			panic("shell: default policy must always construct: " + err.Error())
		}
		s.executor = e
	})
	return s.executor
}

// ExecutionEnforcement lets permission surfaces describe exactly what will
// happen if this tool runs. The bool is false for tools without a
// meaningful enforcement story (everything but shell today).
func (s *Shell) ExecutionEnforcement(ctx context.Context) (sandbox.Enforcement, bool) {
	return s.executorFor().Describe(ctx), true
}

// ensureBackend probes the active executor once per Shell, caching the
// outcome so an unusable backend yields one clear structured error per
// command instead of a confusing per-command failure. A probe aborted by
// caller cancellation is reported but not cached.
func (s *Shell) ensureBackend(ctx context.Context) error {
	s.backendMu.Lock()
	defer s.backendMu.Unlock()
	if s.backendProbed {
		return s.backendErr
	}
	err := s.executorFor().Probe(ctx)
	if ctx.Err() == nil {
		s.backendProbed, s.backendErr = true, err
	}
	return err
}

// CheckBackend verifies that the Bash execution backend is usable (WSL
// availability on Windows; always healthy on Unix) and returns a
// diagnostic error when it isn't. It exists so diagnostics like
// `ff doctor` can report shell problems without executing a command.
func (s *Shell) CheckBackend(ctx context.Context) error {
	return s.ensureBackend(ctx)
}

func (*Shell) Name() string { return "shell" }

func (*Shell) Description() string {
	return "Execute a shell command and return its stdout, stderr, and exit code. " +
		"Commands are executed using GNU Bash. " +
		"Runs in the current project directory unless a working directory is given. " +
		"Commands that require an interactive terminal (editors, pagers, ssh, REPLs, etc.) are not supported."
}

func (*Shell) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute.",
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory to run the command in. Defaults to the current directory.",
			},
			"env": map[string]any{
				"type":        "object",
				"description": "Additional environment variables to set for the command, as key/value string pairs.",
			},
			"timeout_seconds": map[string]any{
				"type":        "number",
				"description": "Maximum number of seconds to allow the command to run before it is killed. Defaults to 30.",
			},
		},
		"required": []string{"command"},
	}
}

// Metadata advertises shell's execution characteristics to the scheduler
// and the model. Shell always requires explicit approval to run.
func (*Shell) Metadata() tools.Metadata {
	return tools.Metadata{
		Timeout:              defaultTimeout,
		SupportsStreaming:    true,
		SupportsCancellation: true,
		SupportsParallel:     true,
		RequiredPermissions:  []tools.Permission{tools.PermissionExecuteShell},
		Retryable:            false, // shell commands are not idempotent in general
	}
}

// Execute runs the command to completion without streaming intermediate
// output. It's a thin wrapper around ExecuteStream with a no-op sink, kept
// so Shell satisfies the plain tools.Tool interface for callers that don't
// care about live output.
func (s *Shell) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	return s.ExecuteStream(ctx, args, nil)
}

// ExecuteStream runs command, invoking onChunk with each sanitized line of
// stdout and stderr as it's produced. onChunk may be nil. It never writes
// to the process's own stdout/stderr/stdin - all communication back to the
// caller is through the returned Result and onChunk, which the runtime
// scheduler turns into EventToolProgress/EventToolFinish/EventToolFailed
// events for the TUI to render.
func (s *Shell) ExecuteStream(ctx context.Context, args map[string]any, onChunk func(tools.StreamChunk)) (tools.Result, error) {
	command, err := tools.StringArg(args, "command")
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(command) == "" {
		return tools.Result{}, &tools.ArgumentError{Field: "command", Reason: "must not be empty"}
	}

	// Commands that need a real TTY (full-screen editors, pagers, remote
	// shells, REPLs...) don't have one here: Stdin is /dev/null and
	// Stdout/Stderr are pipes, not a terminal. Best case they exit
	// immediately with a confusing error; worst case they still probe
	// terminal ioctls or emit full-screen escape codes before noticing
	// there's no tty, which is exactly the kind of stray control sequence
	// that can corrupt the Bubble Tea renderer downstream. Refuse up
	// front with a clear tool error instead of running them at all.
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

	cwd := tools.OptionalStringArg(args, "cwd", "")
	// Directory existence and workspace-scope validation happen inside the
	// executor (Prepare), which is the only place policy lives. Failures
	// come back as typed errors mapped to Results below, so the model sees
	// a clear, retryable message instead of an opaque execution failure.

	// A bad timeout_seconds must be an argument error, not silently ignored:
	// falling back to the 30s default would kill a long command the caller
	// asked to run longer, which looks exactly like "the command never ran".
	timeout := defaultTimeout
	if raw, ok := args["timeout_seconds"]; ok {
		secs, ok := toFloat(raw)
		if !ok || secs <= 0 {
			return tools.Result{}, &tools.ArgumentError{Field: "timeout_seconds", Reason: "must be a positive number of seconds"}
		}
		timeout = time.Duration(secs * float64(time.Second))
	}

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

	// Explicitly isolate the child from the real terminal:
	//   - Stdin: nil makes Go connect it to the null device, so a command
	//     that tries to read interactive input gets an immediate EOF
	//     instead of stealing keystrokes from Bubble Tea's raw-mode input.
	//   - Stdout/Stderr: piped below, never the process's own os.Stdout/
	//     os.Stderr, so nothing the command prints can land on the
	//     terminal outside of Bubble Tea's control.
	cmd.Stdin = nil

	// Put the child in its own process group and take over Cancel so that
	// when runCtx is done (parent cancellation or our own timeout), we
	// kill the whole subtree - not just the immediate `sh` process.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
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

	var stdout, stderr strings.Builder
	pipeDone := make(chan struct{}, 2)
	pipesFinished := make(chan struct{})
	go func() {
		<-pipeDone
		<-pipeDone
		close(pipesFinished)
	}()

	go streamPipe(stdoutReader, "stdout", &stdout, onChunk, pipeDone)
	go streamPipe(stderrReader, "stderr", &stderr, onChunk, pipeDone)

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

	if runCtx.Err() == context.DeadlineExceeded {
		return tools.Result{
			IsError:    true,
			Content:    fmt.Sprintf("command timed out after %s", timeout),
			ExitCode:   -1,
			Stdout:     stdout.String(),
			Stderr:     stderr.String(),
			DurationMs: duration.Milliseconds(),
			Tool:       "shell",
			Command:    command,
		}, nil
	}
	if runCtx.Err() == context.Canceled {
		return tools.Result{
			IsError:    true,
			Content:    "command was cancelled",
			ExitCode:   -1,
			Stdout:     stdout.String(),
			Stderr:     stderr.String(),
			DurationMs: duration.Milliseconds(),
			Tool:       "shell",
			Command:    command,
		}, nil
	}

	// Distinguish backend infrastructure failures (e.g. WSL's distribution
	// failing to start or mount mid-session) from the command's own errors,
	// so the model learns its command never properly ran instead of seeing
	// an unexplained non-zero exit from Bash.
	if !success {
		if detail, hint := sandbox.BackendFailure(stderr.String(), stdout.String()); hint != "" {
			return tools.Result{
				IsError:    true,
				Content:    fmt.Sprintf("shell backend error: WSL could not run the command: %s\n%s", detail, hint),
				ExitCode:   exitCode,
				Stdout:     stdout.String(),
				Stderr:     stderr.String(),
				DurationMs: duration.Milliseconds(),
				Tool:       "shell",
				Command:    command,
			}, nil
		}
	}

	content := stdout.String()
	if stderr.Len() > 0 {
		// Keep stderr visible even on success: callers reading only Content
		// would otherwise lose diagnostics the command wrote to stderr.
		if content == "" {
			content = fmt.Sprintf("stderr:\n%s", stderr.String())
		} else {
			content = fmt.Sprintf("%s\nstderr:\n%s", content, stderr.String())
		}
	}
	if !success {
		content = fmt.Sprintf("command exited with code %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout.String(), stderr.String())
	}

	return tools.Result{
		Content:    content,
		IsError:    !success,
		ExitCode:   exitCode,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMs: duration.Milliseconds(),
		Tool:       "shell",
		Command:    command,
	}, nil
}

// streamPipe copies r line-by-line into both dst (the accumulated buffer
// returned in the Result) and onChunk (live streaming), sanitizing ANSI
// escape and other control sequences out of each line first so nothing
// that could move the cursor, clear the screen, or switch to the
// alternate screen buffer ever reaches the TUI's rendered content. It
// signals done when r is exhausted (EOF or the pipe was closed because
// the process was killed).
//
// It uses bufio.Reader rather than bufio.Scanner so lines of any length
// are captured whole: a Scanner's max token size would silently drop the
// remainder of any line past the cap, losing output.
func streamPipe(r io.Reader, stream string, dst *strings.Builder, onChunk func(tools.StreamChunk), done chan<- struct{}) {
	defer func() { done <- struct{}{} }()

	reader := bufio.NewReader(r)
	for {
		raw, err := reader.ReadString('\n')
		if raw != "" {
			line := sanitizeOutput(strings.TrimSuffix(raw, "\n"))
			dst.WriteString(line)
			dst.WriteByte('\n')
			if onChunk != nil {
				onChunk(tools.StreamChunk{Stream: stream, Data: line})
			}
		}
		if err != nil {
			if err != io.EOF {
				line := sanitizeOutput(fmt.Sprintf("read error: %v", err))
				dst.WriteString(line)
				dst.WriteByte('\n')
				if onChunk != nil {
					onChunk(tools.StreamChunk{Stream: stream, Data: line})
				}
			}
			return
		}
	}
}

// ansiCSI matches "Control Sequence Introducer" sequences (ESC '['
// followed by parameter/intermediate bytes and a final byte), which cover
// cursor movement, screen clearing, and SGR color codes.
var ansiCSI = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// ansiOSC matches "Operating System Command" sequences (ESC ']' ... BEL or
// ST), used for things like setting the terminal title.
var ansiOSC = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// ansiSimple matches the remaining short two-byte escape sequences (e.g.
// ESC 'c' full reset, ESC '7'/'8' save/restore cursor) that aren't CSI or
// OSC sequences.
var ansiSimple = regexp.MustCompile("\x1b[@-Z\\\\\\]^_0-9=>]")

// sanitizeOutput strips ANSI escape sequences and other control
// characters from a line of subprocess output before it's allowed to flow
// into the runtime event system. Bubble Tea composes its own escape
// sequences into every frame it writes to the terminal; if a shell
// command's raw output (which may contain arbitrary bytes) is embedded
// into a rendered string unsanitized, the terminal can't tell the
// difference and will happily execute a stray "clear screen" or
// "switch to alternate buffer" sequence, corrupting the whole UI. This is
// the runtime-side counterpart to never writing subprocess output to
// os.Stdout/os.Stderr directly.
func sanitizeOutput(line string) string {
	line = ansiOSC.ReplaceAllString(line, "")
	line = ansiCSI.ReplaceAllString(line, "")
	line = ansiSimple.ReplaceAllString(line, "")

	var b strings.Builder
	b.Grow(len(line))
	for _, r := range line {
		switch {
		case r == '\t':
			b.WriteRune(r)
		case r < 0x20, r == 0x7f:
			// Drop remaining control characters, including bare ESC
			// (0x1b) left over from a malformed/partial sequence and \r,
			// which terminals treat as "return to column 0" and which
			// could otherwise be used to overwrite adjacent text once
			// rendered.
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// interactiveCommands are programs that always need a real controlling
// terminal to be useful, regardless of what arguments they're given.
var interactiveCommands = map[string]bool{
	"vim": true, "vi": true, "nvim": true, "nano": true, "pico": true,
	"emacs": true, "joe": true,
	"top": true, "htop": true, "btop": true, "watch": true,
	"less": true, "more": true, "man": true,
	"ssh": true, "mosh": true, "telnet": true, "ftp": true, "sftp": true,
	"tmux": true, "screen": true,
	"mysql": true, "psql": true, "sqlite3": true, "redis-cli": true,
}

// bareReplCommands are programs that are only interactive when invoked
// with no arguments (they drop into a REPL); given a script or expression
// to run, they're ordinary non-interactive commands, so these are only
// refused when they're the entire segment.
var bareReplCommands = map[string]bool{
	"python": true, "python3": true, "node": true, "irb": true,
	"pry": true, "ipython": true, "gdb": true, "lldb": true,
	"julia": true, "R": true,
}

// commandSeparators splits a shell command line on the operators that
// chain multiple commands together, so "true && vim" is still caught.
var commandSeparators = regexp.MustCompile(`&&|\|\||[;|]`)

// wrapperCommands are prefix commands that run the following word as the
// real command ("sudo vim", "env python", "nohup top", "command ssh").
// They're skipped when looking for the interactive program in a segment.
var wrapperCommands = map[string]bool{
	"sudo": true, "env": true, "nohup": true, "command": true,
	"xargs": true, "nice": true, "stdbuf": true, "time": true,
}

// detectInteractiveCommand reports whether command invokes a program that
// requires a TTY. It's a deliberately simple heuristic - split on shell
// control operators, skip leading VAR=value assignments and wrapper
// commands, strip quotes from the candidate word, look at the first
// remaining word of each segment - not a full shell parser. That's enough
// to catch the common cases (bare "vim", "cmd1 && ssh host", "FOO=bar
// top", "sudo vim") without taking on the complexity of actually parsing
// shell syntax.
func detectInteractiveCommand(command string) (string, bool) {
	for _, segment := range commandSeparators.Split(command, -1) {
		fields := splitFields(segment)
		for i, field := range fields {
			if isAssignment(field) {
				continue
			}
			name := strings.TrimSuffix(commandBaseName(unquote(field)), ".exe")
			if wrapperCommands[name] {
				continue
			}
			if interactiveCommands[name] {
				return name, true
			}
			if bareReplCommands[name] && i == len(fields)-1 {
				return name, true
			}
			break // first real (non-assignment, non-wrapper) token
		}
	}
	return "", false
}

// splitFields splits a shell segment into words, treating single- and
// double-quoted spans (including spaces inside them) as one field, so a
// quoted program path like `"C:\Program Files\vim.exe"` stays a single
// word for interactive-command detection.
func splitFields(segment string) []string {
	var fields []string
	var current strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if current.Len() > 0 {
			fields = append(fields, current.String())
			current.Reset()
		}
	}
	for _, r := range segment {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case r == ' ' || r == '\t':
			if inSingle || inDouble {
				current.WriteRune(r)
			} else {
				flush()
			}
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return fields
}

// commandBaseName returns the final path component of word, splitting on
// both "/" and "\". Bash commands are agent-authored text, not host
// filesystem paths, so the split can't use filepath.Base: its separator is
// platform-dependent (only ":" and "/" are volume/separator characters on
// Unix builds), which would leave a Windows-style path like
// `C:\Program Files\vim.exe` un-split - and therefore invisible to
// detectInteractiveCommand - when Forcefield itself runs on Linux/macOS.
func commandBaseName(word string) string {
	if i := strings.LastIndexAny(word, `/\`); i >= 0 {
		return word[i+1:]
	}
	return word
}

// unquote strips one level of matching single or double quotes from word,
// so a quoted program name (`"C:\Program Files\vim.exe"`) is still
// recognized.
func unquote(word string) string {
	if len(word) >= 2 {
		if (word[0] == '"' && word[len(word)-1] == '"') || (word[0] == '\'' && word[len(word)-1] == '\'') {
			return word[1 : len(word)-1]
		}
	}
	return word
}

// isAssignment reports whether field looks like a leading shell
// environment assignment, e.g. "FOO=bar", rather than the command itself.
func isAssignment(field string) bool {
	eq := strings.IndexByte(field, '=')
	if eq <= 0 {
		return false
	}
	name := field[:eq]
	for i, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// extraEnvArgs returns the environment variables supplied under the "env"
// argument as K=V pairs, sorted by key for deterministic ordering. Only the
// extras are returned: how they merge into the child's environment is a
// backend decision (appended to os.Environ natively; passed to `env` inside
// WSL, since WSL does not forward arbitrary Windows variables into the
// distribution). A malformed env argument is an argument error rather than
// being silently dropped: the caller asked for variables the command would
// then run without.
func extraEnvArgs(args map[string]any) ([]string, error) {
	raw, ok := args["env"]
	if !ok {
		return nil, nil
	}
	extra, ok := raw.(map[string]any)
	if !ok {
		return nil, &tools.ArgumentError{Field: "env", Reason: "must be an object of string key/value pairs"}
	}

	pairs := make([]string, 0, len(extra))
	for k, v := range extra {
		s, ok := v.(string)
		if !ok {
			return nil, &tools.ArgumentError{Field: "env", Reason: fmt.Sprintf("value for %q must be a string", k)}
		}
		pairs = append(pairs, k+"="+s)
	}
	sort.Strings(pairs)
	return pairs, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
