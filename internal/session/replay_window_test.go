package session

import (
	"strings"
	"testing"
)

// TestProviderMessages_ReplayMatchesModelVisibleWindow pins the resume
// invariant: the originating run truncates a tool result to the 6 KiB
// model-visible window before the model sees it, while the session file
// retains up to 48 KiB for diagnostics. Replaying the saved session must
// produce the identical model-visible content — resume must not silently
// widen the context contribution of that tool result.
func TestProviderMessages_ReplayMatchesModelVisibleWindow(t *testing.T) {
	chdirTemp(t)

	const toolName = "shell"
	// Larger than the model window, smaller than the persisted cap, so
	// the persisted record keeps the full body while the model saw the
	// truncated one.
	output := strings.Repeat("x", 20000)

	// What the originating run showed the model: truncate, then scrub,
	// then fence (mirrors the run loop's recording order).
	modelVisible := FenceToolResult(toolName, TruncateModelToolResult(ScrubContent(output)))

	sess := New()
	sess.AddToolResult("c1", toolName, output)
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The persisted record keeps the larger diagnostic body.
	if len(loaded.Messages[len(loaded.Messages)-1].Content) <= MaxModelToolResultChars {
		t.Fatal("persisted record should retain more than the model-visible window")
	}

	var replayed string
	for _, m := range loaded.ProviderMessages() {
		if string(m.Role) == "tool" && m.ToolCallID == "c1" {
			replayed = m.Content
		}
	}
	if replayed == "" {
		t.Fatal("replayed history lost the tool result")
	}
	if replayed != modelVisible {
		t.Fatalf("replayed len = %d, model-visible len = %d: resume widened the tool-result window", len(replayed), len(modelVisible))
	}
}

// TestProviderMessages_ReplayStaysBounded pins that even a huge tool
// result (past the persisted cap) replays within the model-visible
// window after a save/load round-trip.
func TestProviderMessages_ReplayStaysBounded(t *testing.T) {
	chdirTemp(t)

	sess := New()
	sess.AddToolResult("c1", "shell", strings.Repeat("y", 200000))
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, m := range loaded.ProviderMessages() {
		if string(m.Role) == "tool" && m.ToolCallID == "c1" {
			// 6 KiB window plus marker slack; far below the persisted
			// tens of KiB the pre-fix replay exposed.
			if len(m.Content) > MaxModelToolResultChars+512 {
				t.Fatalf("replayed len = %d, want within the model-visible window", len(m.Content))
			}
			return
		}
	}
	t.Fatal("replayed history lost the tool result")
}
