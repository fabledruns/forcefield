package permissions

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestStdinAsker_RendersChildOrigin pins that a delegated child's ask
// states who is asking (agent plus chain, root first) on the approval
// surface. Top-level runs leave Origin nil and no attribution line
// appears.
func TestStdinAsker_RendersChildOrigin(t *testing.T) {
	var out bytes.Buffer
	asker := &StdinAsker{In: strings.NewReader("n\n"), Out: &out}
	got, err := asker.Ask(context.Background(), Request{
		Tool:      "shell",
		Arguments: map[string]any{},
		Origin:    &Origin{Agent: "research", Chain: "general > research"},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if got != PromptDenyOnce {
		t.Fatalf("decision = %v, want deny-once", got)
	}
	if !strings.Contains(out.String(), "Requested by child agent: general > research") {
		t.Fatalf("prompt output missing child attribution:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Tool:\nshell") {
		t.Fatalf("prompt output missing the tool header:\n%s", out.String())
	}
}

func TestStdinAsker_TopLevelAskHasNoOriginLine(t *testing.T) {
	var out bytes.Buffer
	asker := &StdinAsker{In: strings.NewReader("n\n"), Out: &out}
	if _, err := asker.Ask(context.Background(), Request{Tool: "shell", Arguments: map[string]any{}}); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if strings.Contains(out.String(), "Requested by child agent") {
		t.Fatalf("top-level ask must not render an attribution line:\n%s", out.String())
	}
}

func TestStdinAsker_ChainFallsBackToAgent(t *testing.T) {
	var out bytes.Buffer
	asker := &StdinAsker{In: strings.NewReader("n\n"), Out: &out}
	if _, err := asker.Ask(context.Background(), Request{
		Tool:      "shell",
		Arguments: map[string]any{},
		Origin:    &Origin{Agent: "research"},
	}); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if !strings.Contains(out.String(), "Requested by child agent: research") {
		t.Fatalf("prompt output missing agent-only attribution:\n%s", out.String())
	}
}
