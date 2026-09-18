package session

import (
	"testing"

	"forcefield/internal/providers"

	"github.com/google/uuid"
)

// TestPersistRestartMintedToolCall models a resume across a process
// restart: the file already holds a legacy counter-style synthetic ID
// (call-1, as minted by old binaries), and the restarted process mints
// a restart-safe ID for its next call. Both the new assistant tool call
// and its result must persist alongside the legacy pair — never be
// dropped as a "duplicate".
func TestPersistRestartMintedToolCall(t *testing.T) {
	sess := New()

	// Legacy pair already in the file.
	sess.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "a.txt"}},
	})
	sess.AddToolResult("call-1", "read_file", "old body")
	before := len(sess.Messages)

	// ID as minted after restart: UUID-based, never the counter namespace.
	fresh := "call-" + uuid.NewString()
	if fresh == "call-1" {
		t.Fatal("test setup collision")
	}
	sess.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: fresh, Name: "read_file", Arguments: map[string]any{"path": "b.txt"}},
	})
	if len(sess.Messages) != before+1 {
		t.Fatalf("messages len = %d, want %d: restarted call was dropped as a duplicate", len(sess.Messages), before+1)
	}
	sess.AddToolResult(fresh, "read_file", "new body")
	if len(sess.Messages) != before+2 {
		t.Fatalf("messages len = %d, want %d: restarted result was dropped as a duplicate", len(sess.Messages), before+2)
	}

	if !sess.hasAssistantToolCall(fresh) {
		t.Errorf("new assistant call %q not found in history", fresh)
	}
	if !sess.hasToolResult(fresh) {
		t.Errorf("new tool result %q not found in history", fresh)
	}
	// Legacy pair untouched.
	if !sess.hasAssistantToolCall("call-1") || !sess.hasToolResult("call-1") {
		t.Error("legacy call-1 pair must be preserved")
	}
}
