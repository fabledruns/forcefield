package shell

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/tools"
)

// TestShell_SetLimitsOverrideCapsOutput pins that a configured byte bound
// replaces the 2 MiB default, marks the cut, and reports structured
// metadata — without changing the default path.
func TestShell_SetLimitsOverrideCapsOutput(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	s.SetLimits(tools.Limits{MaxBytes: 1024})

	result, err := s.Execute(context.Background(), map[string]any{
		"command": "seq 1 1000",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	combined := len(result.Stdout) + len(result.Stderr)
	if combined > 1024+128 { // small marker/prefix overhead allowance
		t.Errorf("combined output %d exceeds override 1024", combined)
	}
	if !strings.Contains(result.Content, "truncated") {
		t.Error("override cut lacks a truncation marker")
	}
	if result.Metadata == nil || result.Metadata["truncated"] != true {
		t.Errorf("Metadata = %v, want truncated record", result.Metadata)
	}
	if result.Metadata["limit_bytes"] != 1024 {
		t.Errorf("limit_bytes = %v, want 1024", result.Metadata["limit_bytes"])
	}
	if orig, ok := result.Metadata["original_bytes"].(int); !ok || orig <= 1024 {
		t.Errorf("original_bytes = %v, want > 1024", result.Metadata["original_bytes"])
	}
}

// TestShell_NoTruncationLeavesMetadataEmpty pins that normal output
// carries no truncation metadata.
func TestShell_NoTruncationLeavesMetadataEmpty(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Metadata != nil {
		t.Errorf("Metadata = %v, want nil for untruncated output", result.Metadata)
	}
	if strings.Contains(result.Content, "truncated") {
		t.Errorf("content = %q, want no marker", result.Content)
	}
	if result.ExitCode != 0 || result.Stdout == "" {
		t.Errorf("result = %+v, want exit 0 with stdout", result)
	}
}

// TestShell_StdoutStderrSeparation pins that both streams are captured
// separately and both appear in Content, even on success.
func TestShell_StdoutStderrSeparation(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": "echo out; echo err >&2",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "out" {
		t.Errorf("Stdout = %q, want out", result.Stdout)
	}
	if strings.TrimSpace(result.Stderr) != "err" {
		t.Errorf("Stderr = %q, want err", result.Stderr)
	}
	if !strings.Contains(result.Content, "out") || !strings.Contains(result.Content, "err") {
		t.Errorf("Content = %q, want both streams visible", result.Content)
	}
}

// TestShell_StreamBudgetBoundsLiveOutput pins the P1 stream-cap fix: a
// command emitting far more than the byte bound must have its live
// onChunk output bounded by the same budget as the accumulated result,
// with exactly one truncation marker. Before the fix the stream path was
// uncapped (every line forwarded), letting chatty processes grow the
// event stream and TUI transcript without limit.
func TestShell_StreamBudgetBoundsLiveOutput(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	const bound = 1024
	s.SetLimits(tools.Limits{MaxBytes: bound})

	var mu sync.Mutex
	var streamed, chunks, markers int
	onChunk := func(c tools.StreamChunk) {
		mu.Lock()
		defer mu.Unlock()
		chunks++
		streamed += len(c.Data)
		if strings.Contains(c.Data, "truncated") {
			markers++
		}
	}

	// seq 1 1000 emits ~3.9 KiB, roughly 4x the bound.
	result, err := s.ExecuteStream(context.Background(), map[string]any{
		"command": "seq 1 1000",
	}, onChunk)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// One marker chunk on top of the data budget, plus one line of slack
	// for the line that trips the cap.
	if streamed > bound+len(streamTruncateMarker)+256 {
		t.Errorf("streamed %d bytes with bound %d: live output not capped", streamed, bound)
	}
	if markers != 1 {
		t.Errorf("got %d truncation markers in %d chunks, want exactly 1", markers, chunks)
	}
	// The stream must stop early, not forward all ~1000 lines.
	if chunks >= 1000 {
		t.Errorf("got %d chunks for 1000 output lines: stream never stopped", chunks)
	}
	// The accumulated result keeps its existing cap behavior.
	if combined := len(result.Stdout) + len(result.Stderr); combined > bound+128 {
		t.Errorf("combined result %d exceeds bound %d", combined, bound)
	}
	if result.Metadata == nil || result.Metadata["truncated"] != true {
		t.Errorf("Metadata = %v, want truncated record", result.Metadata)
	}
}

// TestShell_SmallOutputStreamsFullyUnmarked pins that the stream budget
// changes nothing for ordinary output: every line streams, no marker.
func TestShell_SmallOutputStreamsFullyUnmarked(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()

	var mu sync.Mutex
	var streamed, chunks int
	var sawMarker bool
	onChunk := func(c tools.StreamChunk) {
		mu.Lock()
		defer mu.Unlock()
		chunks++
		streamed += len(c.Data)
		if strings.Contains(c.Data, "truncated") {
			sawMarker = true
		}
	}

	result, err := s.ExecuteStream(context.Background(), map[string]any{
		"command": "echo hello-stream",
	}, onChunk)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true: %s", result.Content)
	}

	mu.Lock()
	defer mu.Unlock()
	if chunks != 1 {
		t.Errorf("got %d chunks, want 1", chunks)
	}
	if sawMarker {
		t.Error("small output must not carry a truncation marker")
	}
	if streamed != len("hello-stream") {
		t.Errorf("streamed %d bytes, want %d (line without newline)", streamed, len("hello-stream"))
	}
}

// TestShell_TimeoutOverrideReflectedInMetadata pins that SetLimits
// timeouts flow into the scheduler-visible Metadata.
func TestShell_TimeoutOverrideReflectedInMetadata(t *testing.T) {
	s := NewShell()
	if got := s.Metadata().Timeout; got != tools.DefaultShellTimeout {
		t.Errorf("default Metadata timeout = %v, want %v", got, tools.DefaultShellTimeout)
	}
	s.SetLimits(tools.Limits{Timeout: 45 * time.Second})
	if got := s.Metadata().Timeout; got != 45*time.Second {
		t.Errorf("override Metadata timeout = %v, want 45s", got)
	}
	if got := s.ToolLimits().MaxBytes; got != tools.DefaultShellMaxBytes {
		t.Errorf("ToolLimits MaxBytes = %d, want default (partial override keeps rest)", got)
	}
}
