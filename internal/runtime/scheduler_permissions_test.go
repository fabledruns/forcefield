package runtime

import (
	"context"
	"testing"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// memPermStore is an in-memory permissions.Store for tests, avoiding any
// dependency on config.yaml.
type memPermStore struct {
	rules permissions.Rules
}

func (s *memPermStore) Load() (permissions.Rules, error) { return s.rules, nil }
func (s *memPermStore) Save(r permissions.Rules) error   { s.rules = r; return nil }

func newTestPermManager(t *testing.T, def permissions.Decision, overrides map[string]permissions.Decision) *permissions.Manager {
	t.Helper()
	store := &memPermStore{rules: permissions.Rules{Default: def, Tools: overrides}}
	m, err := permissions.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestScheduler_AllowedToolExecutes(t *testing.T) {
	tool := &fixedResultTool{name: "read_file"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Deny, map[string]permissions.Decision{"read_file": permissions.Allow})
	s := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "read_file"}}, func(Event) bool { return true })

	if len(results) != 1 || results[0].IsError {
		t.Fatalf("expected successful result, got %#v", results)
	}
	if results[0].Content != "read_file" {
		t.Errorf("tool did not actually run: %#v", results[0])
	}
}

func TestScheduler_DeniedToolNeverExecutes(t *testing.T) {
	ran := false
	tool := &fnTool{name: "shell", fn: func() { ran = true }}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Allow, map[string]permissions.Decision{"shell": permissions.Deny})
	s := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "shell"}}, func(Event) bool { return true })

	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("expected a permission-denied error result, got %#v", results)
	}
	if ran {
		t.Error("tool executed despite being denied")
	}
}

func TestScheduler_DefaultAppliesWhenNoOverride(t *testing.T) {
	ran := false
	tool := &fnTool{name: "write_file", fn: func() { ran = true }}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Deny, nil) // no per-tool rule for write_file
	s := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "write_file"}}, func(Event) bool { return true })

	if !results[0].IsError || ran {
		t.Fatalf("expected default (deny) to apply, got IsError=%v ran=%v", results[0].IsError, ran)
	}
}

func TestScheduler_AskAllowOnceDoesNotPersist(t *testing.T) {
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "shell"}}, func(Event) bool { return true })
	if results[0].IsError {
		t.Fatalf("expected allow-once to execute, got %#v", results[0])
	}
	if got := perms.Check("shell"); got != permissions.Ask {
		t.Errorf("Check(shell) after allow-once = %v, want Ask (unchanged)", got)
	}
}

func TestScheduler_AskAlwaysAllowPersists(t *testing.T) {
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		return permissions.PromptAlwaysAllow, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "shell"}}, func(Event) bool { return true })
	if results[0].IsError {
		t.Fatalf("expected always-allow to execute, got %#v", results[0])
	}
	// Always allow is now session-scoped, not persisted globally.
	if got := perms.Check("shell"); got != permissions.Ask {
		t.Errorf("Check(shell) after always-allow = %v, want Ask (session-scoped, not persisted)", got)
	}
	// Second call should be allowed via session without asking again
	called := false
	s.asker = permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		called = true
		return permissions.PromptDenyOnce, nil
	})
	results = s.Run(context.Background(), []providers.ToolCall{{ID: "2", Name: "shell"}}, func(Event) bool { return true })
	if results[0].IsError {
		t.Fatalf("expected session-scoped allow to persist for second call, got %#v", results[0])
	}
	if called {
		t.Error("asker should not be called for session-scoped allow")
	}
}

func TestScheduler_AskAlwaysDenyPersistsAndSkipsFutureAsk(t *testing.T) {
	asked := 0
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asked++
		return permissions.PromptAlwaysDeny, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	// First call: asked, denied, and session-scoped.
	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "shell"}}, func(Event) bool { return true })
	if !results[0].IsError {
		t.Fatalf("expected always-deny to deny the call, got %#v", results[0])
	}
	// Second call: should be denied from session-scoped rule without prompting again.
	results = s.Run(context.Background(), []providers.ToolCall{{ID: "2", Name: "shell"}}, func(Event) bool { return true })
	if !results[0].IsError {
		t.Fatalf("expected session deny to apply, got %#v", results[0])
	}
	if asked != 1 {
		t.Errorf("asker invoked %d times, want 1 (second call should use session rule)", asked)
	}
	// Global store should not have been updated (session-scoped)
	if got := perms.Check("shell"); got != permissions.Ask {
		t.Errorf("global Check(shell) = %v, want Ask (session-scoped, not persisted)", got)
	}
}

func TestScheduler_AskWithNoAskerFailsClosed(t *testing.T) {
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	s := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "shell"}}, func(Event) bool { return true })
	if !results[0].IsError {
		t.Fatalf("expected fail-closed deny with no asker, got %#v", results[0])
	}
}

// fnTool is a minimal tools.Tool that records whether it ran, for
// asserting a denied tool never actually executes.
type fnTool struct {
	name string
	fn   func()
}

func (t *fnTool) Name() string                { return t.name }
func (t *fnTool) Description() string         { return "fn" }
func (t *fnTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *fnTool) Execute(ctx context.Context, _ map[string]any) (tools.Result, error) {
	t.fn()
	return tools.Result{Content: t.name}, nil
}
