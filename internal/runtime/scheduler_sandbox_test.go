package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// enforcementTool reports a sandbox Enforcement the way the shell tool
// does, so tests can verify the scheduler forwards it to askers.
type enforcementTool struct {
	enforcement sandbox.Enforcement
}

func (enforcementTool) Name() string        { return "shell" }
func (enforcementTool) Description() string { return "shell with policy" }
func (enforcementTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (enforcementTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	return tools.Result{Content: "ran"}, nil
}
func (t enforcementTool) ExecutionEnforcement(context.Context) (sandbox.Enforcement, bool) {
	return t.enforcement, true
}

// capturingAsker records the Request it was shown.
type capturingAsker struct{ got permissions.Request }

func (a *capturingAsker) Ask(_ context.Context, req permissions.Request) (permissions.Prompt, error) {
	a.got = req
	return permissions.PromptAllowOnce, nil
}

func TestScheduler_AttachesExecutionEnforcementToAsk(t *testing.T) {
	want := sandbox.Enforcement{
		Mode:            sandbox.ModeWSL,
		Network:         sandbox.NetworkDisabled,
		CwdPinned:       true,
		NetworkEnforced: true,
	}
	manager := tools.NewManager(tools.NewRegistry())
	if err := manager.Register(enforcementTool{enforcement: want}); err != nil {
		t.Fatal(err)
	}
	perms := newTestPermManager(t, permissions.Ask, nil)
	asker := &capturingAsker{}
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "shell"}}, func(Event) bool { return true })
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("results = %+v, want success", results)
	}

	if asker.got.Execution == nil {
		t.Fatal("asker received no Execution enforcement; approval UI would have nothing honest to show")
	}
	if asker.got.Execution.Mode != sandbox.ModeWSL || !asker.got.Execution.NetworkEnforced {
		t.Errorf("asker saw %+v, want the executor's report", asker.got.Execution)
	}
	lines := strings.Join(asker.got.Execution.SummaryLines(), "\n")
	if !strings.Contains(lines, "WSL") || !strings.Contains(lines, "enforced") {
		t.Errorf("rendered lines must reflect WSL + enforced network:\n%s", lines)
	}
}

func TestScheduler_ToolsWithoutExecutionStoryGetNoBlock(t *testing.T) {
	manager := tools.NewManager(tools.NewRegistry())
	if err := manager.Register(&fixedResultTool{name: "read_file"}); err != nil {
		t.Fatal(err)
	}
	perms := newTestPermManager(t, permissions.Ask, nil)
	asker := &capturingAsker{}
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "read_file"}}, func(Event) bool { return true })

	if asker.got.Execution != nil {
		t.Errorf("non-execution tool carried an Enforcement block: %+v", asker.got.Execution)
	}
}
