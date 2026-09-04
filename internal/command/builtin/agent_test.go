package builtin

import (
	"strings"
	"testing"

	"forcefield/internal/command"
)

func TestAgent_ListWithoutArgs(t *testing.T) {
	ctx := &fakeContext{
		agent: "general",
		agentList: []command.AgentSummary{
			{Name: "coding", Description: "coding"},
			{Name: "general", Description: "general"},
		},
	}
	if err := NewAgent().Execute(ctx, nil); err != nil {
		t.Fatalf("Agent.Execute list: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "coding") || !strings.Contains(out, "general") {
		t.Fatalf("list missing agents: %q", out)
	}
	if !strings.Contains(out, "Active: general") {
		t.Fatalf("missing active: %q", out)
	}
}

func TestAgent_SwitchWithArg(t *testing.T) {
	ctx := &fakeContext{
		agent: "general",
		agentList: []command.AgentSummary{
			{Name: "coding", Description: "coding agent"},
			{Name: "general", Description: "general"},
		},
	}
	if err := NewAgent().Execute(ctx, []string{"coding"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ctx.agent != "coding" {
		t.Fatalf("agent = %q, want coding", ctx.agent)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "✓ Agent: coding") {
		t.Fatalf("missing confirmation: %q", out)
	}
}

func TestAgent_RejectsTooManyArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewAgent().Execute(ctx, []string{"a", "b"}); err == nil {
		t.Fatalf("expected error for too many args")
	}
}

func TestAgent_PropagatesError(t *testing.T) {
	ctx := &fakeContext{setAgentErr: assertError("fail")}
	if err := NewAgent().Execute(ctx, []string{"coding"}); err == nil {
		t.Fatalf("expected error")
	}
}

func assertError(msg string) error {
	return &testErr{msg}
}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }
