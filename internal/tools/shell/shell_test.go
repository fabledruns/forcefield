package shell

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/tools"
)

func commandSeparator() string {
	if runtime.GOOS == "windows" {
		return "&"
	}
	return ";"
}

func stderrRedirect() string {
	if runtime.GOOS == "windows" {
		return "1>&2"
	}
	return ">&2"
}

func commandChain(commands ...string) string {
	return strings.Join(commands, " "+commandSeparator()+" ")
}

func cancellableCommand() string {
	if runtime.GOOS == "windows" {
		return "timeout /T 30 /NOBREAK >NUL"
	}
	return commandChain("(sleep 30 &)", "sleep 30")
}

func TestShell_CapturesStdout(t *testing.T) {
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true, content: %s", result.Content)
	}
	if strings.TrimSpace(result.Stdout) != "hello" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "hello")
	}
	if strings.TrimSpace(result.Content) != "hello" {
		t.Errorf("Content = %q, want %q", result.Content, "hello")
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Tool != "shell" {
		t.Errorf("Tool = %q, want shell", result.Tool)
	}
}

func TestShell_CapturesStderrSeparately(t *testing.T) {
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": commandChain("echo out", "echo err "+stderrRedirect()),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "out" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "out")
	}
	if strings.TrimSpace(result.Stderr) != "err" {
		t.Errorf("Stderr = %q, want %q", result.Stderr, "err")
	}
}

func TestShell_ReportsNonZeroExitCode(t *testing.T) {
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": "exit 7",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false, want true for a non-zero exit")
	}
	if result.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", result.ExitCode)
	}
}

func TestShell_CommandFailureDoesNotReturnGoError(t *testing.T) {
	// A failing shell command is a normal (if unsuccessful) tool result,
	// not a Go-level error - the scheduler and model both need to be able
	// to see stdout/stderr/exit code, which a plain `error` can't carry.
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": "no-such-command-xyz",
	})
	if err != nil {
		t.Fatalf("Execute() returned a Go error = %v, want a failed Result instead", err)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false, want true")
	}
}

func TestShell_ContextCancellationStopsTheCommandAndItsChildren(t *testing.T) {
	s := NewShell()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan tools.Result, 1)
	go func() {
		// Spawn a detached grandchild that would otherwise outlive `sh`
		// itself, to prove killProcessGroup reaches it too.
		result, _ := s.Execute(ctx, map[string]any{
			"command": cancellableCommand(),
		})
		done <- result
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case result := <-done:
		if !result.IsError {
			t.Errorf("result.IsError = false, want true after cancellation")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Execute did not return within 15s of context cancellation; process (group) was not killed")
	}
}

func TestShell_TimeoutIsEnforced(t *testing.T) {
	s := NewShell()
	start := time.Now()
	result, err := s.Execute(context.Background(), map[string]any{
		"command":         "sleep 5",
		"timeout_seconds": 0.2,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Execute took %s, want well under the 5s sleep (timeout should have cut it short)", elapsed)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false, want true for a timed-out command")
	}
}

func TestShell_ConcurrentExecutionsAreIndependent(t *testing.T) {
	s := NewShell()

	const n = 5
	var wg sync.WaitGroup
	results := make([]tools.Result, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = s.Execute(context.Background(), map[string]any{
				"command": "echo " + string(rune('a'+i)),
			})
		}()
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("run %d: Execute() error = %v", i, errs[i])
		}
		want := string(rune('a' + i))
		if got := strings.TrimSpace(results[i].Stdout); got != want {
			t.Errorf("run %d: Stdout = %q, want %q (no cross-talk between concurrent runs)", i, got, want)
		}
	}
}

func TestShell_StreamsChunksThroughCallbackNotTerminal(t *testing.T) {
	// This is the TUI-safety contract: all output must flow through
	// onChunk (which the scheduler turns into events for the TUI), never
	// touch a real file descriptor directly.
	s := NewShell()

	var mu sync.Mutex
	var chunks []tools.StreamChunk
	onChunk := func(c tools.StreamChunk) {
		mu.Lock()
		defer mu.Unlock()
		chunks = append(chunks, c)
	}

	result, err := s.ExecuteStream(context.Background(), map[string]any{
		"command": commandChain("echo one", "echo two "+stderrRedirect()),
	}, onChunk)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true, content: %s", result.Content)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2: %#v", len(chunks), chunks)
	}
	var sawStdout, sawStderr bool
	for _, c := range chunks {
		switch c.Stream {
		case "stdout":
			sawStdout = true
			if strings.TrimSpace(c.Data) != "one" {
				t.Errorf("stdout chunk = %q, want %q", c.Data, "one")
			}
		case "stderr":
			sawStderr = true
			if strings.TrimSpace(c.Data) != "two" {
				t.Errorf("stderr chunk = %q, want %q", c.Data, "two")
			}
		}
	}
	if !sawStdout || !sawStderr {
		t.Errorf("expected both a stdout and a stderr chunk, got %#v", chunks)
	}
}

func TestShell_RefusesInteractiveCommands(t *testing.T) {
	s := NewShell()

	cases := []string{
		"vim file.go",
		"top",
		"ssh example.com",
		"echo fine && ssh example.com",
		"less README.md",
		"python", // bare REPL
	}

	for _, command := range cases {
		result, err := s.Execute(context.Background(), map[string]any{"command": command})
		if err != nil {
			t.Fatalf("command %q: Execute() unexpected Go error = %v", command, err)
		}
		if !result.IsError {
			t.Errorf("command %q: result.IsError = false, want true (should be refused)", command)
		}
	}
}

func TestShell_AllowsNonInteractiveLookalikes(t *testing.T) {
	s := NewShell()

	// These share a program name with a blocked interactive tool but are
	// given a script/argument, so they're ordinary non-interactive runs
	// and must not be refused.
	result, err := s.Execute(context.Background(), map[string]any{
		"command": "python3 -c \"print(1)\"",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Errorf("result.IsError = true, want false: %s", result.Content)
	}
}

func TestSanitizeOutput_StripsANSIAndControlSequences(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "sgr color codes",
			input: "\x1b[31mred\x1b[0m text",
			want:  "red text",
		},
		{
			name:  "cursor movement and clear screen",
			input: "\x1b[2J\x1b[Hhello",
			want:  "hello",
		},
		{
			name:  "osc window title",
			input: "\x1b]0;my title\x07visible",
			want:  "visible",
		},
		{
			name:  "carriage return progress bar overwrite",
			input: "50%\rdone",
			want:  "50%done",
		},
		{
			name:  "plain text is untouched",
			input: "just plain output",
			want:  "just plain output",
		},
		{
			name:  "tabs are preserved",
			input: "a\tb",
			want:  "a\tb",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeOutput(tc.input); got != tc.want {
				t.Errorf("sanitizeOutput(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestShell_SanitizesANSIInStreamedOutput(t *testing.T) {
	s := NewShell()

	var mu sync.Mutex
	var data []string
	onChunk := func(c tools.StreamChunk) {
		mu.Lock()
		defer mu.Unlock()
		data = append(data, c.Data)
	}

	// printf with an embedded raw escape sequence (color code) - simulates
	// a CLI tool that emits ANSI color even though its stdout is a pipe.
	result, err := s.ExecuteStream(context.Background(), map[string]any{
		"command": `printf '\033[31mred\033[0m\n'`,
	}, onChunk)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	if strings.Contains(result.Stdout, "\x1b") {
		t.Errorf("Result.Stdout still contains a raw escape byte: %q", result.Stdout)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, line := range data {
		if strings.Contains(line, "\x1b") {
			t.Errorf("streamed chunk still contains a raw escape byte: %q", line)
		}
	}
}

func TestDetectInteractiveCommand(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"vim main.go", true},
		{"ssh host 'ls'", true},
		{"echo hi && top", true},
		{"FOO=bar ssh host", true},
		{"python", true},
		{"python3 script.py", false},
		{"ls -la", false},
		{"git status", false},
		{"echo top", false},
	}

	for _, tc := range cases {
		_, got := detectInteractiveCommand(tc.command)
		if got != tc.want {
			t.Errorf("detectInteractiveCommand(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}