package runtime

import (
	"context"
	"testing"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// Test that strict validation happens before permission, so a wrong-type
// argument does not trigger an Ask prompt and does not use the dangerous
// default.
func TestScheduler_StrictValidationBeforePermission(t *testing.T) {
	// Use a real filesystem tool that has a defined schema: read_file requires path string
	manager := tools.NewManager(tools.NewRegistry())
	// Register a minimal read_file-like tool with strict schema
	// We can use the actual filesystem tool via builtin, but for unit test we use a stub
	// with the same InputSchema as read_file
	stub := &strictTool{
		name: "read_file",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
	}
	if err := manager.Register(stub); err != nil {
		t.Fatal(err)
	}
	perms := newTestPermManager(t, permissions.Ask, nil)
	asked := false
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asked = true
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: 0})

	// Wrong type for path: should be rejected as ArgumentError before Ask
	calls := []providers.ToolCall{{ID: "1", Name: "read_file", Arguments: map[string]any{"path": 12345}}}
	results := s.Run(context.Background(), calls, func(Event) bool { return true })
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("expected validation error, got %+v", results[0])
	}
	if asked {
		t.Error("validation should happen before permission; asker should not have been called")
	}
	if results[0].Err == nil {
		t.Error("expected ArgumentError in result.Err")
	}

	// Unknown field should also be rejected before permission
	asked = false
	calls = []providers.ToolCall{{ID: "2", Name: "read_file", Arguments: map[string]any{"path": "x", "extra": "y"}}}
	results = s.Run(context.Background(), calls, func(Event) bool { return true })
	if !results[0].IsError {
		t.Fatalf("expected unknown field error, got %+v", results[0])
	}
	if asked {
		t.Error("unknown field should be rejected before permission")
	}

	// Correct args should still go through permission (Ask)
	asked = false
	calls = []providers.ToolCall{{ID: "3", Name: "read_file", Arguments: map[string]any{"path": "x"}}}
	results = s.Run(context.Background(), calls, func(Event) bool { return true })
	if !asked {
		t.Error("correct args should trigger Ask")
	}
	// The stub's Execute will be called and return success
	if results[0].IsError {
		t.Fatalf("correct args should succeed, got %+v", results[0])
	}
}

type strictTool struct {
	name   string
	schema map[string]any
}

func (s *strictTool) Name() string                { return s.name }
func (s *strictTool) Description() string         { return "test" }
func (s *strictTool) InputSchema() map[string]any { return s.schema }
func (s *strictTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	return tools.Result{Content: "ok"}, nil
}
