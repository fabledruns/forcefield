package permissions

import (
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

// Phase 6 agreement: MCP tools carry no Execution report, so approval
// surfaces label them from one shared notice instead of rendering an
// empty risk block that reads as safe.
func TestMCPUnsandboxedNotice(t *testing.T) {
	got := MCPUnsandboxedNotice("mcp__docs__search")
	if got == "" {
		t.Fatal("qualified MCP tool must produce a notice")
	}
	if !strings.Contains(got, "UNSANDBOXED") {
		t.Errorf("notice must state UNSANDBOXED plainly: %q", got)
	}
	if want := sandbox.MCPUnsandboxedLimitation().Detail; got != want {
		t.Errorf("notice must equal the canonical limitation verbatim:\n got: %q\nwant: %q", got, want)
	}
	for _, native := range []string{"shell", "read_file", "mcp__bad", "mcp_docs_search", ""} {
		if got := MCPUnsandboxedNotice(native); got != "" {
			t.Errorf("MCPUnsandboxedNotice(%q) = %q, want empty", native, got)
		}
	}
}
