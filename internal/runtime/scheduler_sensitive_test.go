package runtime

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

func TestSensitiveFileRequiresApprovalEvenWhenAllowed(t *testing.T) {
	tool := &fixedResultTool{name: "read_file"}
	manager := newTestManager(t, tool)
	// read_file globally allowed
	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{"read_file": permissions.Allow})
	asked := false
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asked = true
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	// Non-sensitive path should not ask (allowed directly)
	asked = false
	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "read_file", Arguments: map[string]any{"path": "normal.txt"}}}, func(Event) bool { return true })
	if results[0].IsError {
		t.Fatalf("normal file should be allowed without ask, got %#v", results[0])
	}
	if asked {
		t.Error("asker should not be called for normal file when allowed")
	}

	// Sensitive .env should force Ask even though Allow
	asked = false
	results = s.Run(context.Background(), []providers.ToolCall{{ID: "2", Name: "read_file", Arguments: map[string]any{"path": ".env"}}}, func(Event) bool { return true })
	if results[0].IsError {
		t.Fatalf("sensitive .env with AllowOnce should execute, got %#v", results[0])
	}
	if !asked {
		t.Error("sensitive .env should have forced an Ask prompt even when globally allowed")
	}

	// Also test .env.local, id_rsa, .pem
	for _, sensitive := range []string{".env.local", "my.pem", "key.key", ".ssh/id_rsa", ".aws/credentials"} {
		asked = false
		results = s.Run(context.Background(), []providers.ToolCall{{ID: "3", Name: "read_file", Arguments: map[string]any{"path": sensitive}}}, func(Event) bool { return true })
		if !asked {
			t.Errorf("path %q should be considered sensitive and require Ask", sensitive)
		}
		if results[0].IsError {
			t.Errorf("path %q with AllowOnce should succeed", sensitive)
		}
	}

	// Sensitive with DenyOnce should be denied
	asker2 := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		return permissions.PromptDenyOnce, nil
	})
	s2 := newScheduler(manager, perms, asker2, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	results = s2.Run(context.Background(), []providers.ToolCall{{ID: "4", Name: "read_file", Arguments: map[string]any{"path": ".env"}}}, func(Event) bool { return true })
	if !results[0].IsError {
		t.Error("sensitive .env denied should be error")
	}
}

func TestConcurrentAskRequestsDoNotDeadlock(t *testing.T) {
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	var asked atomic.Int64
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		// Simulate user taking 50ms to answer
		time.Sleep(50 * time.Millisecond)
		asked.Add(1)
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 4, MaxRetries: 0, BaseBackoff: time.Millisecond})

	calls := []providers.ToolCall{
		{ID: "1", Name: "shell", Arguments: map[string]any{"command": "echo 1"}},
		{ID: "2", Name: "shell", Arguments: map[string]any{"command": "echo 2"}},
		{ID: "3", Name: "shell", Arguments: map[string]any{"command": "echo 3"}},
		{ID: "4", Name: "shell", Arguments: map[string]any{"command": "echo 4"}},
	}

	done := make(chan []ToolResult, 1)
	go func() {
		done <- s.Run(context.Background(), calls, func(Event) bool { return true })
	}()

	select {
	case results := <-done:
		if len(results) != 4 {
			t.Fatalf("results len %d, want 4", len(results))
		}
		for i, r := range results {
			if r.IsError {
				t.Errorf("call %d result IsError true: %#v", i, r)
			}
		}
		if asked.Load() != 4 {
			t.Errorf("asked count %d, want 4 (all prompts delivered)", asked.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked: 4 concurrent Asks did not complete within 5s")
	}
}

func TestConcurrentSensitiveAskStillSerialized(t *testing.T) {
	tool := &fixedResultTool{name: "read_file"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	var asked atomic.Int64
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		time.Sleep(20 * time.Millisecond)
		asked.Add(1)
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 4, MaxRetries: 0, BaseBackoff: time.Millisecond})
	calls := []providers.ToolCall{
		{ID: "1", Name: "read_file", Arguments: map[string]any{"path": ".env"}},
		{ID: "2", Name: "read_file", Arguments: map[string]any{"path": ".env.local"}},
		{ID: "3", Name: "read_file", Arguments: map[string]any{"path": "secret.pem"}},
		{ID: "4", Name: "read_file", Arguments: map[string]any{"path": ".ssh/id_rsa"}},
	}
	done := make(chan []ToolResult, 1)
	go func() { done <- s.Run(context.Background(), calls, func(Event) bool { return true }) }()
	select {
	case results := <-done:
		for i, r := range results {
			if r.IsError {
				t.Errorf("sensitive call %d IsError %v", i, r)
			}
		}
		if asked.Load() != 4 {
			t.Errorf("asked %d, want 4", asked.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked on sensitive concurrent")
	}
}

func TestScheduler_ScrubsSecretsFromToolResults(t *testing.T) {
	tool := &secretContentTool{content: "here is sk-12345678901234567890abcdef and more"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Allow, map[string]permissions.Decision{"read_file": permissions.Allow})
	s := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "read_file", Arguments: map[string]any{"path": "normal.txt"}}}, func(Event) bool { return true })
	if len(results) != 1 {
		t.Fatalf("expected 1 result")
	}
	if strings.Contains(results[0].Content, "sk-") {
		t.Errorf("tool result should be scrubbed, got %q", results[0].Content)
	}
	if !strings.Contains(strings.ToLower(results[0].Content), "redacted") {
		t.Errorf("scrubbed result should contain [redacted], got %q", results[0].Content)
	}
}

type secretContentTool struct {
	content string
}

func (t *secretContentTool) Name() string                { return "read_file" }
func (t *secretContentTool) Description() string         { return "test" }
func (t *secretContentTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *secretContentTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	return tools.Result{Content: t.content}, nil
}
