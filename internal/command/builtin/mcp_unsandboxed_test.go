package builtin

import (
	"strings"
	"testing"
)

// Phase 6 agreement: the management surface states the unsandboxed
// posture every time servers are listed, so configured-but-unopened
// servers never read as confined.
func TestMCP_ListStatesUnsandboxed(t *testing.T) {
	ctx := &fakeContext{mcpServers: mcpTestServers()}
	if err := NewMCP().Execute(ctx, []string{"list"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := mcpJoinedLines(ctx)
	if !strings.Contains(out, "unsandboxed") {
		t.Errorf("server list must state the unsandboxed posture:\n%s", out)
	}
}
