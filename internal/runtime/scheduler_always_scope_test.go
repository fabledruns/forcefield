package runtime

import (
	"context"
	"testing"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
)

func runOneCall(t *testing.T, s *scheduler, id, name string, args map[string]any) ToolResult {
	t.Helper()
	results := s.Run(context.Background(), []providers.ToolCall{{ID: id, Name: name, Arguments: args}}, func(Event) bool { return true })
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	return results[0]
}

// Approving Always for operation A allows A again, but B still requires approval.
func TestScheduler_AlwaysAllowScopedToCommand(t *testing.T) {
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	asks := 0
	s := newScheduler(manager, perms, permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks++
		if asks == 1 {
			return permissions.PromptAlwaysAllow, nil
		}
		return permissions.PromptAllowOnce, nil
	}), SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	// Operation A approved with Always.
	if r := runOneCall(t, s, "1", "shell", map[string]any{"command": "echo hi"}); r.IsError {
		t.Fatalf("first A should execute, got %#v", r)
	}
	if asks != 1 {
		t.Fatalf("asks = %d, want 1 after first A", asks)
	}

	// Same operation A again: no new prompt.
	if r := runOneCall(t, s, "2", "shell", map[string]any{"command": "echo hi"}); r.IsError {
		t.Fatalf("second A should reuse Always approval, got %#v", r)
	}
	if asks != 1 {
		t.Fatalf("asks = %d, want 1 (A reused without prompting)", asks)
	}

	// Different operation B: must prompt again.
	if r := runOneCall(t, s, "3", "shell", map[string]any{"command": "rm -rf /"}); r.IsError {
		t.Fatalf("B with AllowOnce should execute, got %#v", r)
	}
	if asks != 2 {
		t.Fatalf("asks = %d, want 2 (B required fresh approval)", asks)
	}

	// B again still prompts (only AllowOnce, not Always).
	if r := runOneCall(t, s, "4", "shell", map[string]any{"command": "rm -rf /"}); r.IsError {
		t.Fatalf("B again should execute, got %#v", r)
	}
	if asks != 3 {
		t.Fatalf("asks = %d, want 3 (B was only allow-once)", asks)
	}

	// A still allowed without prompting.
	if r := runOneCall(t, s, "5", "shell", map[string]any{"command": "echo hi"}); r.IsError {
		t.Fatalf("A should still be allowed, got %#v", r)
	}
	if asks != 3 {
		t.Fatalf("asks = %d, want 3 (A still cached)", asks)
	}
}

// Semantically identical commands share one approval.
func TestScheduler_AlwaysAllowNormalizesCommand(t *testing.T) {
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	asks := 0
	s := newScheduler(manager, perms, permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks++
		return permissions.PromptAlwaysAllow, nil
	}), SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	if r := runOneCall(t, s, "1", "shell", map[string]any{"command": "  echo hi  "}); r.IsError {
		t.Fatalf("first should execute, got %#v", r)
	}
	if r := runOneCall(t, s, "2", "shell", map[string]any{"command": "echo hi"}); r.IsError {
		t.Fatalf("normalized repeat should execute, got %#v", r)
	}
	if asks != 1 {
		t.Fatalf("asks = %d, want 1 (whitespace-only difference shares approval)", asks)
	}
}

// Always does not persist across sessions (schedulers).
func TestScheduler_AlwaysDoesNotPersistAcrossSessions(t *testing.T) {
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	s1 := newScheduler(manager, perms, permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		return permissions.PromptAlwaysAllow, nil
	}), SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	if r := runOneCall(t, s1, "1", "shell", map[string]any{"command": "echo hi"}); r.IsError {
		t.Fatalf("s1 first should execute, got %#v", r)
	}
	// Global store unchanged (session-scoped, non-persistent).
	if got := perms.Check("shell"); got != permissions.Ask {
		t.Fatalf("global Check(shell) = %v, want Ask", got)
	}

	// New scheduler = new session: must prompt again even for same command.
	asked := false
	s2 := newScheduler(manager, perms, permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asked = true
		return permissions.PromptAllowOnce, nil
	}), SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	if r := runOneCall(t, s2, "1", "shell", map[string]any{"command": "echo hi"}); r.IsError {
		t.Fatalf("s2 same command should execute after ask, got %#v", r)
	}
	if !asked {
		t.Fatal("new session should have prompted again for the same command")
	}
}

// Non-Always behavior unchanged: AllowOnce doesn't persist; per-tool
// Always preserved for tools without an operation identifier; Deny still broad.
func TestScheduler_NonAlwaysBehaviorUnchanged(t *testing.T) {
	// AllowOnce for shell does not grant a second, identical command.
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	asks := 0
	s := newScheduler(manager, perms, permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks++
		return permissions.PromptAllowOnce, nil
	}), SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	if r := runOneCall(t, s, "1", "shell", map[string]any{"command": "echo hi"}); r.IsError {
		t.Fatalf("first should execute, got %#v", r)
	}
	if r := runOneCall(t, s, "2", "shell", map[string]any{"command": "echo hi"}); r.IsError {
		t.Fatalf("second should execute after second ask, got %#v", r)
	}
	if asks != 2 {
		t.Fatalf("asks = %d, want 2 (AllowOnce never persists)", asks)
	}

	// Tools without a command identifier keep per-tool Always.
	rf := &fixedResultTool{name: "read_file"}
	m2 := newTestManager(t, rf)
	p2 := newTestPermManager(t, permissions.Ask, nil)
	asks2 := 0
	s2 := newScheduler(m2, p2, permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks2++
		return permissions.PromptAlwaysAllow, nil
	}), SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	if r := runOneCall(t, s2, "1", "read_file", map[string]any{"path": "a.txt"}); r.IsError {
		t.Fatalf("read a.txt should execute, got %#v", r)
	}
	if r := runOneCall(t, s2, "2", "read_file", map[string]any{"path": "b.txt"}); r.IsError {
		t.Fatalf("read b.txt should reuse per-tool Always, got %#v", r)
	}
	if asks2 != 1 {
		t.Fatalf("asks2 = %d, want 1 (non-command tools preserve per-tool Always)", asks2)
	}

	// AlwaysDeny stays broad (fail-closed): denying one command denies another.
	asks3 := 0
	s3 := newScheduler(manager, perms, permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks3++
		return permissions.PromptAlwaysDeny, nil
	}), SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	if r := runOneCall(t, s3, "1", "shell", map[string]any{"command": "echo hi"}); !r.IsError {
		t.Fatalf("denied command should error, got %#v", r)
	}
	if r := runOneCall(t, s3, "2", "shell", map[string]any{"command": "echo other"}); !r.IsError {
		t.Fatalf("different command should also be denied without new prompt, got %#v", r)
	}
	if asks3 != 1 {
		t.Fatalf("asks3 = %d, want 1 (deny stays broad)", asks3)
	}
}

// shell_job start is also operation-scoped.
func TestScheduler_AlwaysAllowScopedToShellJobStart(t *testing.T) {
	tool := &fixedResultTool{name: "shell_job"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	asks := 0
	s := newScheduler(manager, perms, permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asks++
		if asks == 1 {
			return permissions.PromptAlwaysAllow, nil
		}
		return permissions.PromptAllowOnce, nil
	}), SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	if r := runOneCall(t, s, "1", "shell_job", map[string]any{"action": "start", "command": "sleep 5"}); r.IsError {
		t.Fatalf("first start should execute, got %#v", r)
	}
	if r := runOneCall(t, s, "2", "shell_job", map[string]any{"action": "start", "command": "sleep 5"}); r.IsError {
		t.Fatalf("same start should reuse approval, got %#v", r)
	}
	if asks != 1 {
		t.Fatalf("asks = %d, want 1", asks)
	}
	if r := runOneCall(t, s, "3", "shell_job", map[string]any{"action": "start", "command": "rm -rf /"}); r.IsError {
		t.Fatalf("different start should execute after fresh ask, got %#v", r)
	}
	if asks != 2 {
		t.Fatalf("asks = %d, want 2 (different command re-prompted)", asks)
	}
}
