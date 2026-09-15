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

// boundaryScheduler builds a scheduler whose only tool is a real
// write_file confined to ws, with Ask as the default decision.
func boundaryScheduler(t *testing.T, ws string, asker permissions.Asker) *scheduler {
	t.Helper()
	wf := filesystem.NewWriteFileWithPolicy(sandbox.Policy{Workspace: ws})
	manager := newTestManager(t, wf)
	perms := newTestPermManager(t, permissions.Ask, nil)
	return newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
}

func runWrite(t *testing.T, s *scheduler, id, path string, events *[]Event) ToolResult {
	t.Helper()
	results := s.Run(context.Background(), []providers.ToolCall{{
		ID: id, Name: "write_file",
		Arguments: map[string]any{"path": path, "content": "hello"},
	}}, func(e Event) bool {
		if events != nil {
			*events = append(*events, e)
		}
		return true
	})
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one", results)
	}
	return results[0]
}

// TestScheduler_BoundaryDeniesBeforePrompt is the scheduler-level
// regression test for the reported incident: an outside-workspace write
// is denied without ever prompting, reports failure (not a permission
// denial), and creates nothing.
func TestScheduler_BoundaryDeniesBeforePrompt(t *testing.T) {
	ws := t.TempDir()
	var asks atomic.Int64
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks.Add(1)
		return permissions.PromptAllowOnce, nil
	})
	s := boundaryScheduler(t, ws, asker)

	outside := filepath.Join(t.TempDir(), "escape.txt")
	var events []Event
	res := runWrite(t, s, "1", outside, &events)
	if !res.IsError {
		t.Fatalf("outside write succeeded, want boundary denial: %#v", res)
	}
	if !strings.Contains(res.Content, "workspace") && !strings.Contains(res.Content, "outside") {
		t.Errorf("denial should name the boundary, got %q", res.Content)
	}
	if asks.Load() != 0 {
		t.Errorf("asker called %d times, want 0 (denial precedes the prompt)", asks.Load())
	}
	sawFailed, sawDenied := false, false
	for _, e := range events {
		if e.Type == EventToolFailed {
			sawFailed = true
		}
		if e.Type == EventToolDenied {
			sawDenied = true
		}
	}
	if !sawFailed || sawDenied {
		t.Errorf("events failed=%v denied=%v, want a tool failure, not a permission denial", sawFailed, sawDenied)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("denied write created %q", outside)
	}
}

// TestScheduler_BoundaryDeniesPosixAbsolute is the exact reported
// incident through the full scheduler path: write_file /go/main.go on
// Windows resolves to a drive-root path outside the workspace and must
// be rejected before the permission prompt, creating nothing.
func TestScheduler_BoundaryDeniesPosixAbsolute(t *testing.T) {
	ws := t.TempDir()
	var asks atomic.Int64
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks.Add(1)
		return permissions.PromptAllowOnce, nil
	})
	s := boundaryScheduler(t, ws, asker)

	probe := string(filepath.Separator) + "go" + string(filepath.Separator) + "main.go"
	res := runWrite(t, s, "1", probe, nil)
	if !res.IsError {
		t.Fatalf("write_file %q succeeded, want boundary denial", probe)
	}
	if asks.Load() != 0 {
		t.Errorf("asker called %d times, want 0", asks.Load())
	}
}

// TestScheduler_AlwaysAllowCannotEscapeBoundary proves approval scope
// can never widen the boundary: after Always-allowing an inside write,
// an outside write is still denied without prompting and creates
// nothing.
func TestScheduler_AlwaysAllowCannotEscapeBoundary(t *testing.T) {
	ws := t.TempDir()
	var asks atomic.Int64
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks.Add(1)
		return permissions.PromptAlwaysAllow, nil
	})
	s := boundaryScheduler(t, ws, asker)

	inside := filepath.Join("sub", "note.txt")
	if res := runWrite(t, s, "1", inside, nil); res.IsError {
		t.Fatalf("inside write failed: %#v", res)
	}
	if asks.Load() != 1 {
		t.Fatalf("asks = %d, want 1 for the inside write", asks.Load())
	}
	data, err := os.ReadFile(filepath.Join(ws, "sub", "note.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("inside write not persisted: %v %q", err, string(data))
	}

	// The stored Always allow must not authorize this: denial, no
	// prompt, no file.
	outside := filepath.Join(t.TempDir(), "escape.txt")
	res := runWrite(t, s, "2", outside, nil)
	if !res.IsError {
		t.Fatalf("outside write succeeded under Always allow, want boundary denial")
	}
	if asks.Load() != 1 {
		t.Errorf("asks = %d, want still 1 (no second prompt for the denial)", asks.Load())
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("denied write created %q", outside)
	}
}

// TestScheduler_ResolvedPathReachesPrompt proves the approval surface
// sees the canonical target: the prompt for an inside write carries the
// absolute resolved path, not just the raw model spelling.
func TestScheduler_ResolvedPathReachesPrompt(t *testing.T) {
	ws := t.TempDir()
	var gotResolved string
	var asks atomic.Int64
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks.Add(1)
		gotResolved = req.ResolvedPath
		return permissions.PromptAllowOnce, nil
	})
	s := boundaryScheduler(t, ws, asker)

	if res := runWrite(t, s, "1", filepath.Join("sub", "note.txt"), nil); res.IsError {
		t.Fatalf("inside write failed: %#v", res)
	}
	if asks.Load() != 1 {
		t.Fatalf("asks = %d, want 1 prompt for the inside write", asks.Load())
	}
	want, err := sandbox.ResolveWithinWorkspace(ws, filepath.Join("sub", "note.txt"))
	if err != nil {
		t.Fatalf("ResolveWithinWorkspace: %v", err)
	}
	if gotResolved != want {
		t.Errorf("prompt ResolvedPath = %q, want %q", gotResolved, want)
	}
}

// TestScheduler_InsideWriteStillPromptsAndSucceeds guards against
// over-blocking: legitimate inside-workspace writes still prompt once
// under Ask and then execute.
func TestScheduler_InsideWriteStillPromptsAndSucceeds(t *testing.T) {
	ws := t.TempDir()
	var asks atomic.Int64
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks.Add(1)
		if req.ResolvedPath == "" {
			t.Errorf("prompt for an inside write carries no ResolvedPath")
		}
		return permissions.PromptAllowOnce, nil
	})
	s := boundaryScheduler(t, ws, asker)

	if res := runWrite(t, s, "1", "note.txt", nil); res.IsError {
		t.Fatalf("inside write failed: %#v", res)
	}
	if asks.Load() != 1 {
		t.Errorf("asks = %d, want exactly 1 prompt", asks.Load())
	}
	data, err := os.ReadFile(filepath.Join(ws, "note.txt"))
	if err != nil || string(data) != "hello" {
		t.Errorf("inside write not persisted: %v %q", err, string(data))
	}
}
