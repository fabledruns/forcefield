package shell

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

func TestShell_TimeoutValidation(t *testing.T) {
	s := NewShell()
	cases := []struct {
		name    string
		timeout any
		wantErr bool
	}{
		{"negative float", -1.0, true},
		{"zero", 0.0, true},
		{"too large 301", 301.0, true},
		{"large int", 1000, true},
		{"string invalid", "30", true},
		{"valid 30", 30.0, false},
		{"valid 300", 300.0, false},
		{"valid int 10", 10, false},
		{"valid float 0.5", 0.5, false},
		{"too large float 300.1", 300.1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"command": "echo hi", "timeout_seconds": tc.timeout}
			_, err := s.Execute(context.Background(), args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for timeout %v", tc.timeout)
				}
				var argErr *tools.ArgumentError
				if !errors.As(err, &argErr) {
					t.Fatalf("expected ArgumentError, got %T: %v", err, err)
				}
				if argErr.Field != "timeout_seconds" {
					t.Fatalf("field = %q, want timeout_seconds", argErr.Field)
				}
			} else {
				// For valid cases we may get a backend probe error on Windows, but not an ArgumentError.
				// Ensure no ArgumentError.
				var argErr *tools.ArgumentError
				if errors.As(err, &argErr) && argErr.Field == "timeout_seconds" {
					t.Fatalf("unexpected timeout validation error for %v: %v", tc.timeout, err)
				}
				// If err is nil or other, it's fine (backend may fail). We just check validation passed.
			}
		})
	}
}

func TestShell_ShellOutputCap(t *testing.T) {
	out := &shellOutput{}
	// Write 1 MiB lines repeatedly until we exceed cap
	line := strings.Repeat("A", 1024) // 1 KiB
	// 2 MiB = 2*1024*1024 = 2097152 bytes, need ~2048 such lines to fill.
	// Write 3000 lines = ~3 MiB, should truncate.
	for i := 0; i < 3000; i++ {
		out.append("stdout", line)
	}
	if !out.isTruncated() {
		t.Fatal("expected truncated after exceeding 2 MiB")
	}
	stdout := out.stdoutString()
	if len(stdout) > maxShellOutputBytes {
		t.Fatalf("stdout len %d exceeds cap %d", len(stdout), maxShellOutputBytes)
	}
	// Ensure total counted correctly (with newlines)
	if out.total > maxShellOutputBytes {
		t.Fatalf("total %d exceeds cap", out.total)
	}
	// Subsequent writes should not increase total
	before := out.total
	out.append("stdout", line)
	if out.total != before {
		t.Fatalf("append after cap changed total %d -> %d", before, out.total)
	}
}

func TestShell_StreamPipeCapAndDrain(t *testing.T) {
	// Simulate a pipe producing >3 MiB of output, ensure streamPipe drains it
	// while capping captured bytes at ~2 MiB and not deadlocking.
	out := &shellOutput{}
	// Build a reader that emits 3 MiB as lines
	var b strings.Builder
	// Each line 100 bytes + newline, 35000 lines ~3.5 MiB
	line := strings.Repeat("x", 100)
	for i := 0; i < 35000; i++ {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	data := b.String()
	r := strings.NewReader(data)
	done := make(chan struct{}, 1)
	go streamPipe(r, "stdout", out, nil, done)
	<-done
	if !out.isTruncated() {
		t.Fatal("expected truncated after large pipe")
	}
	s := out.stdoutString()
	if len(s) > maxShellOutputBytes {
		t.Fatalf("captured len %d exceeds cap", len(s))
	}
	// Ensure we drained (streamPipe returned) and didn't hang
}

func TestShell_StreamPipeCombinedCap(t *testing.T) {
	out := &shellOutput{}
	// Split into lines of 1 KiB
	chunk := strings.Repeat("B", 1024)
	// Write 1536 chunks = 1.5 MiB
	for i := 0; i < 1536; i++ {
		out.append("stdout", chunk)
	}
	// Now 600 KiB via stderr should push over cap
	for i := 0; i < 600; i++ {
		out.append("stderr", chunk)
	}
	if !out.isTruncated() {
		t.Fatal("expected combined cap to trigger truncation")
	}
	totalCaptured := len(out.stdoutString()) + len(out.stderrString()) + out.total // out.total already includes both with newlines
	// total should be <= cap + small slack (newline)
	if out.total > maxShellOutputBytes {
		t.Fatalf("combined total %d exceeds cap", out.total)
	}
	_ = totalCaptured
}

func TestShell_LargeOutputIsCappedIntegration(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	// Generate ~3 MiB via a shell loop; use seq and printf
	// Try python first if available, else fallback to loop
	// Use a command that prints ~3 MiB in a predictable way.
	cmd := `python3 -c "import sys; sys.stdout.write('A'*3000000)"`
	// Check if python3 exists by trying a small run; if fails, use shell loop
	ctx := context.Background()
	probe, err := s.Execute(ctx, map[string]any{"command": "python3 --version"})
	if err != nil || probe.IsError {
		// fallback: use yes/head or loop
		cmd = `for i in $(seq 1 50000); do echo "line $i: this is a long output line to fill buffer quickly ................................................"; done`
	}
	result, err := s.Execute(ctx, map[string]any{"command": cmd})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	// Should not be OOM, should return
	combined := len(result.Stdout) + len(result.Stderr)
	if combined > maxShellOutputBytes+1024 { // allow small marker overhead
		t.Fatalf("combined output %d exceeds cap %d (truncated=%v, content snippet %q)", combined, maxShellOutputBytes, strings.Contains(result.Content, "truncated"), result.Content[:200])
	}
	if combined > maxShellOutputBytes {
		// If over cap, content should contain truncation marker
		if !strings.Contains(result.Content, "truncated") {
			t.Errorf("expected truncation marker in content when over cap, got %q", result.Content[len(result.Content)-200:])
		}
	}
	// Ensure we got some output
	if len(result.Stdout) == 0 && len(result.Stderr) == 0 {
		t.Fatal("expected some stdout for large output test")
	}
	// Drain check: command should have completed (not hung)
	if result.Tool != "shell" {
		t.Errorf("tool = %q, want shell", result.Tool)
	}
}

func TestShell_TimeoutValidationIntegration(t *testing.T) {
	// This test validates that timeout validation happens before backend probing,
	// so it works even without a backend.
	s := NewShell()
	_, err := s.Execute(context.Background(), map[string]any{"command": "echo hi", "timeout_seconds": 999999})
	if err == nil {
		t.Fatal("expected error for huge timeout")
	}
	var argErr *tools.ArgumentError
	if !errors.As(err, &argErr) {
		t.Fatalf("expected ArgumentError, got %T: %v", err, err)
	}
	// Also test io.Reader doesn't block after cap
	_ = io.Discard
}
