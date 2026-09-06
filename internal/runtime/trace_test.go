package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/trace"
)

// readTraceLines decodes every JSONL line of a run file.
func readTraceLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad JSONL: %v (%q)", err, line)
		}
		out = append(out, m)
	}
	return out
}

func traceTypes(lines []map[string]any) []string {
	var out []string
	for _, l := range lines {
		if ty, ok := l["type"].(string); ok {
			out = append(out, ty)
		}
	}
	return out
}

// TestTrace_RecordsFullTurn shapes the P1.11 contract end to end: one
// tool turn produces an ordered run_start/turn/tool/done spine, the
// file stays local JSONL, and secrets in args and results never land.
func TestTrace_RecordsFullTurn(t *testing.T) {
	dir := t.TempDir()
	p := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{ToolCalls: []providers.ToolCall{{
			ID:   "c1",
			Name: "echo",
			Arguments: map[string]any{
				"value": "leak sk-12345678901234567890abcdef here",
			},
		}}}, {Done: true}},
		{{Text: "done sk-12345678901234567890abcdef", Done: true}},
	}}
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   newTestManager(t, echoTool{}),
		scheduler: newScheduler(newTestManager(t, echoTool{}), nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
		tracer:    trace.New(true, dir),
	}

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range events {
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("trace files = %v, want exactly 1 run file", files)
	}
	lines := readTraceLines(t, filepath.Join(dir, files[0].Name()))
	types := traceTypes(lines)
	for _, want := range []string{"run_start", "turn_start", "tool_start", "tool_end", "turn_end", "run_done"} {
		found := false
		for _, got := range types {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("trace types %v lack %q", types, want)
		}
	}
	// Order: run_start first, run_done last.
	if types[0] != "run_start" || types[len(types)-1] != "run_done" {
		t.Errorf("trace spine not bracketed: %v", types)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if strings.Contains(string(raw), "sk-12345678901234567890abcdef") {
		t.Errorf("trace leaked secret:\n%s", raw)
	}
}

// TestTrace_DisabledWritesNothing pins the default: no tracer means no
// directory, no files, and zero behavior change.
func TestTrace_DisabledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	p := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{Text: "done", Done: true}},
	}}
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   newTestManager(t, echoTool{}),
		scheduler: newScheduler(newTestManager(t, echoTool{}), nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
		tracer:    trace.New(false, dir),
	}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range events {
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("disabled tracing wrote files: %v", entries)
	}
}

// TestTrace_CancelRecorded pins that cancellation surfaces in the trace
// as a run_error (diagnosing interrupted runs is the point).
func TestTrace_CancelRecorded(t *testing.T) {
	dir := t.TempDir()
	rt := &Runtime{
		provider:  hangingProvider{},
		agent:     newTestRuntime(hangingProvider{}).agent,
		manager:   newTestManager(t, echoTool{}),
		scheduler: newScheduler(newTestManager(t, echoTool{}), nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
		tracer:    trace.New(true, dir),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		events, err := rt.StreamChat(ctx, []providers.Message{{Role: providers.UserRole, Content: "hi"}})
		if err != nil {
			return
		}
		for range events {
		}
	}()
	cancel()
	<-done

	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("trace files = %v, want 1", files)
	}
	types := traceTypes(readTraceLines(t, filepath.Join(dir, files[0].Name())))
	found := false
	for _, ty := range types {
		if ty == "run_error" || ty == "turn_error" {
			found = true
		}
	}
	if !found {
		t.Errorf("cancelled run lacks error lines: %v", types)
	}
}
