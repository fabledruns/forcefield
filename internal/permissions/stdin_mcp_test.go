package permissions

import (
	"context"
	"strings"
	"testing"
)

// Phase 6 agreement: the headless prompt labels MCP calls UNSANDBOXED
// while native tools without an execution story show no such notice.
func TestStdinAskerShowsMCPNotice(t *testing.T) {
	var out strings.Builder
	asker := &StdinAsker{In: strings.NewReader("n\n"), Out: &out}
	p, err := asker.Ask(context.Background(), Request{Tool: "mcp__docs__search", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if p != PromptDenyOnce {
		t.Errorf("prompt = %v, want deny-once for %q", p, "n")
	}
	if !strings.Contains(out.String(), "UNSANDBOXED") {
		t.Errorf("MCP prompt must state UNSANDBOXED:\n%s", out.String())
	}

	out.Reset()
	asker = &StdinAsker{In: strings.NewReader("n\n"), Out: &out}
	if _, err := asker.Ask(context.Background(), Request{Tool: "read_file", Arguments: map[string]any{}}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.Contains(out.String(), "UNSANDBOXED") {
		t.Errorf("native prompt must not carry the MCP notice:\n%s", out.String())
	}
}
