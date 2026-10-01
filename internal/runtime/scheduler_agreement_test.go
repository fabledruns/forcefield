package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/sandbox"
	"forcefield/internal/tools/filesystem"
)

// Phase 6 agreement: the sandbox boundary is not a permission decision.
// It runs before every layer below and no Allow — session, persisted,
// or one-shot — can authorize an escape. The denial surfaces as a tool
// failure (not a permission denial) without prompting or executing.
func TestScheduler_PrecedenceBoundaryOverAllAllows(t *testing.T) {
	ws := t.TempDir()
	wf := filesystem.NewWriteFileWithPolicy(sandbox.Policy{Workspace: ws})
	manager := newTestManager(t, wf)
	// Persisted Allow for the tool.
	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{
		"write_file": permissions.Allow,
	})
	var asks atomic.Int64
	asker := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		asks.Add(1)
		return permissions.PromptAlwaysAllow, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	outside := filepath.Join(t.TempDir(), "escape.txt")
	call := providers.ToolCall{ID: "1", Name: "write_file",
		Arguments: map[string]any{"path": outside, "content": "hello"}}
	// Session Allow at operation scope too: boundary must still win.
	s.setSessionDecision(sessionAllowKey(call), permissions.Allow)

	var events []Event
	results := s.Run(context.Background(), []providers.ToolCall{call}, func(e Event) bool {
		events = append(events, e)
		return true
	})
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("outside write must be denied, got %#v", results)
	}
	if asks.Load() != 0 {
		t.Errorf("asker called %d times, want 0: boundary precedes every allow", asks.Load())
	}
	sawFailed := false
	for _, e := range events {
		if e.Type == EventToolDenied {
			t.Errorf("boundary denial surfaced as permission denial: %+v", e)
		}
		if e.Type == EventToolFailed {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Error("want EventToolFailed for boundary denial")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("denied write created %q", outside)
	}
}

// Session Deny outranks persisted Allow: the tool never runs and no
// prompt is needed.
func TestScheduler_PrecedenceSessionDenyOverPersistedAllow(t *testing.T) {
	ran := false
	manager := newTestManager(t, &fnTool{name: "shell", fn: func() { ran = true }})
	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{
		"shell": permissions.Allow,
	})
	var asks atomic.Int64
	asker := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		asks.Add(1)
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	s.setSessionDecision("shell", permissions.Deny)

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "shell"}}, func(Event) bool { return true })
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("session-denied call must fail, got %#v", results)
	}
	if ran {
		t.Error("denied tool executed")
	}
	if asks.Load() != 0 {
		t.Errorf("asker called %d times, want 0", asks.Load())
	}
}

// Session Allow (operation-scoped) outranks persisted Check, including
// persisted Deny: the approved operation runs without re-prompting.
func TestScheduler_PrecedenceSessionAllowOverPersistedDeny(t *testing.T) {
	ran := false
	manager := newTestManager(t, &fnTool{name: "shell", fn: func() { ran = true }})
	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{
		"shell": permissions.Deny,
	})
	var asks atomic.Int64
	asker := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		asks.Add(1)
		return permissions.PromptDenyOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	call := providers.ToolCall{ID: "1", Name: "shell"}
	s.setSessionDecision(sessionAllowKey(call), permissions.Allow)

	results := s.Run(context.Background(), []providers.ToolCall{call}, func(Event) bool { return true })
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("session-allowed call must run, got %#v", results)
	}
	if !ran {
		t.Error("session-allowed tool did not execute")
	}
	if asks.Load() != 0 {
		t.Errorf("asker called %d times, want 0", asks.Load())
	}
}

// Sensitive escalation outranks both Allows: a persisted Allow plus a
// session Allow on a sensitive read still forces the prompt. The asker
// observes the call (escalation happened) and grants it once.
func TestScheduler_PrecedenceSensitiveEscalatesOverAllAllows(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, ".env"), []byte("K=V"), 0o600); err != nil {
		t.Fatal(err)
	}
	rf := filesystem.NewReadFileWithPolicy(sandbox.Policy{Workspace: ws})
	manager := newTestManager(t, rf)
	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{
		"read_file": permissions.Allow,
	})
	var asks atomic.Int64
	asker := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		asks.Add(1)
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	call := providers.ToolCall{ID: "1", Name: "read_file",
		Arguments: map[string]any{"path": ".env"}}
	s.setSessionDecision(sessionAllowKey(call), permissions.Allow)

	results := s.Run(context.Background(), []providers.ToolCall{call}, func(Event) bool { return true })
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("prompt-granted sensitive read must succeed, got %#v", results)
	}
	if asks.Load() != 1 {
		t.Errorf("asker called %d times, want exactly 1: escalation must force the prompt", asks.Load())
	}
}

// Headless with no asker still denies at the boundary first: the error
// names the boundary, not the missing prompt.
func TestScheduler_HeadlessBoundaryBeforeAskerMissing(t *testing.T) {
	ws := t.TempDir()
	wf := filesystem.NewWriteFileWithPolicy(sandbox.Policy{Workspace: ws})
	manager := newTestManager(t, wf)
	perms := newTestPermManager(t, permissions.Ask, nil)
	s := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	outside := filepath.Join(t.TempDir(), "escape.txt")
	results := s.Run(context.Background(), []providers.ToolCall{{
		ID: "1", Name: "write_file",
		Arguments: map[string]any{"path": outside, "content": "hello"},
	}}, func(Event) bool { return true })
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("outside write must be denied, got %#v", results)
	}
	if !strings.Contains(results[0].Content, "workspace") && !strings.Contains(results[0].Content, "outside") {
		t.Errorf("denial should name the boundary, got %q", results[0].Content)
	}
	if strings.Contains(results[0].Content, "no permission prompt") {
		t.Errorf("boundary must precede the asker-missing error, got %q", results[0].Content)
	}
}
