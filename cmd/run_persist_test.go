package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// TestMapRunEvent_TranslatesToolEvidence pins the runtime-event to
// orchestration-event translation: identity, counters, and durations
// cross; content, arguments, and paths never do; paints and run
// terminal events (classified separately) produce no line.
func TestMapRunEvent_TranslatesToolEvidence(t *testing.T) {
	cases := []struct {
		name string
		ev   runtime.Event
		want orchevents.Event
		ok   bool
	}{
		{
			name: "tool start",
			ev:   runtime.Event{Type: runtime.EventToolStart, ToolCall: &providers.ToolCall{ID: "call-1", Name: "read_file"}},
			want: orchevents.Event{Type: orchevents.ToolStarted, CallID: "call-1", Tool: "read_file"},
			ok:   true,
		},
		{
			name: "tool ok with duration and attempt",
			ev:   runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{ToolCallID: "call-1", Name: "read_file", Success: true, Duration: 1200 * time.Millisecond, Attempt: 2}},
			want: orchevents.Event{Type: orchevents.ToolCompleted, CallID: "call-1", Tool: "read_file", DurationMS: 1200, Attempt: 2},
			ok:   true,
		},
		{
			name: "finish with error flag counts as failed",
			ev:   runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{ToolCallID: "call-2", Name: "shell", IsError: true}},
			want: orchevents.Event{Type: orchevents.ToolFailed, CallID: "call-2", Tool: "shell"},
			ok:   true,
		},
		{
			name: "failed event keeps error text and attempt",
			ev:   runtime.Event{Type: runtime.EventToolFailed, ToolResult: &runtime.ToolResult{ToolCallID: "call-3", Name: "write_file", Attempt: 3, Err: errors.New("disk full")}},
			want: orchevents.Event{Type: orchevents.ToolFailed, CallID: "call-3", Tool: "write_file", Attempt: 3, Error: "disk full"},
			ok:   true,
		},
		{
			name: "denied",
			ev:   runtime.Event{Type: runtime.EventToolDenied, ToolResult: &runtime.ToolResult{ToolCallID: "call-4", Name: "shell"}},
			want: orchevents.Event{Type: orchevents.ToolDenied, CallID: "call-4", Tool: "shell"},
			ok:   true,
		},
		{
			name: "cancelled",
			ev:   runtime.Event{Type: runtime.EventToolCancelled, ToolResult: &runtime.ToolResult{ToolCallID: "call-5", Name: "shell"}},
			want: orchevents.Event{Type: orchevents.ToolCancelled, CallID: "call-5", Tool: "shell"},
			ok:   true,
		},
		{
			name: "shell exit code rides metadata",
			ev:   runtime.Event{Type: runtime.EventToolFinish, ToolResult: &runtime.ToolResult{ToolCallID: "call-6", Name: "shell", Success: true, ExitCode: 3, HasExitCode: true}},
			want: orchevents.Event{Type: orchevents.ToolCompleted, CallID: "call-6", Tool: "shell", Meta: map[string]any{"exit_code": 3}},
			ok:   true,
		},
		{
			name: "missing tool call is skipped",
			ev:   runtime.Event{Type: runtime.EventToolStart},
			ok:   false,
		},
		{
			name: "missing tool result is skipped",
			ev:   runtime.Event{Type: runtime.EventToolFinish},
			ok:   false,
		},
		{
			name: "text paint carries no evidence",
			ev:   runtime.Event{Type: runtime.EventText, Text: "hello"},
			ok:   false,
		},
		{
			name: "thinking paint carries no evidence",
			ev:   runtime.Event{Type: runtime.EventThinking, Thinking: "deep"},
			ok:   false,
		},
		{
			name: "tool progress carries no evidence",
			ev:   runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{ToolCallID: "call-1", Stream: "stdout", Data: "x"}},
			ok:   false,
		},
		{
			name: "run terminal events are classified separately",
			ev:   runtime.Event{Type: runtime.EventDone, Status: runtime.StatusVerified},
			ok:   false,
		},
	}
	for _, tc := range cases {
		got, ok := mapRunEvent(tc.ev)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if !tc.ok {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: event = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestRunHeadlessSession_PersistsSessionAndEventLog proves the headless
// path persists both the session file and the orchestration event log
// even for a run that fails immediately: evidence of failure is the
// whole point. Uses a real runtime whose config points at a closed
// local port, so the provider turn fails fast and deterministically
// without any external service.
func TestRunHeadlessSession_PersistsSessionAndEventLog(t *testing.T) {
	isolateResumeHome(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".forcefield"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgYAML := "model:\n  provider: ollama\n  endpoint: http://127.0.0.1:9\n  name: probe-model\n"
	if err := os.WriteFile(filepath.Join(home, ".forcefield", "config.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// Hermetic working directory: session + event files land here.
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origWd) }()

	rt, err := runtime.New()
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	_, _, _, runErr := runHeadlessSession(context.Background(), rt, []providers.Message{{Role: providers.UserRole, Content: "audit note"}})
	if runErr == nil {
		t.Fatal("expected the provider-less run to fail")
	}

	// The session exists and carries the task message.
	entries, err := os.ReadDir(".forcefield/sessions")
	if err != nil {
		t.Fatalf("read sessions dir: %v", err)
	}
	var ids []string
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".json") {
			ids = append(ids, strings.TrimSuffix(name, ".json"))
		}
	}
	if len(ids) != 1 {
		t.Fatalf("got %d session files (%v), want exactly 1", len(ids), ids)
	}
	sess, err := session.Load(ids[0])
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if len(sess.Messages) != 1 || sess.Messages[0].Content != "audit note" {
		t.Errorf("session messages = %+v, want exactly one user task", sess.Messages)
	}

	// The event log beside it records the honest failure lifecycle,
	// stamped with the same session id.
	events, err := orchevents.ReadAll(filepath.Join(".forcefield/sessions", ids[0]+".events.jsonl"))
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("got %d events, want at least run_started + terminal", len(events))
	}
	if events[0].Type != orchevents.RunStarted {
		t.Errorf("first event = %q, want run_started", events[0].Type)
	}
	if events[0].TaskChars != len("audit note") {
		t.Errorf("task_chars = %d, want %d", events[0].TaskChars, len("audit note"))
	}
	last := events[len(events)-1]
	if last.Type != orchevents.RunFailed {
		t.Fatalf("terminal event = %q, want run_failed", last.Type)
	}
	if last.Outcome != orchevents.OutcomeError || last.Error == "" {
		t.Errorf("failed run evidence incomplete: outcome=%q error=%q", last.Outcome, last.Error)
	}
	if last.ExitCode == nil || *last.ExitCode != 1 {
		t.Errorf("exit code stamp = %v, want 1", last.ExitCode)
	}
}
