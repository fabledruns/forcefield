package session

// Replay-window tail parity regression tests (B1 follow-through): the
// persisted tool result is replayed through Session.ProviderMessages to the
// provider on resume. The redesign must apply the same head+tail window on
// replay, so a resumed run still sees the tool result's final bytes and stays
// inside MaxModelToolResultChars.
//
// Authored against the PRE-FIX baseline; uses only existing exported symbols
// and helpers (New, AddToolResult, Save, Load, ProviderMessages,
// FenceToolResult, TruncateModelToolResult, ScrubContent, chdirTemp).

import (
	"strings"
	"testing"
)

// replayToolContent builds synthetic tool-result content with a fixed total
// size and a unique ASCII tail sentinel as the final bytes.
func replayToolContent(total int, sentinel string) string {
	return strings.Repeat("p", total-(len(sentinel))) + sentinel
}

// TestProviderMessages_ReplayKeepsPreciousTail pins that resume preserves the
// tail window of a saved tool result: the provider must still see the final
// bytes (e.g. the byte-capped truncation note of a search tool) after a
// save/load round-trip. Baseline: replay is head-only, the tail is lost.
func TestProviderMessages_ReplayKeepsPreciousTail(t *testing.T) {
	chdirTemp(t)
	const tailSentinel = "RPLY_TAIL_PRESERVED_7A3C"
	content := replayToolContent(9000, tailSentinel)

	sess := New()
	sess.AddToolResult("c1", "search_files", content)
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	found := false
	for _, m := range loaded.ProviderMessages() {
		if string(m.Role) == "tool" && m.ToolCallID == "c1" {
			found = true
			if !strings.Contains(m.Content, tailSentinel) {
				t.Fatalf("replayed tool result (%d bytes) lost the tail sentinel; resume must replay the redesigned head+tail window:\n%s", len(m.Content), m.Content[len(m.Content)-400:])
			}
			// 42 bytes: "<tool_result tool=\"search_files\">\n … \n</tool_result>"
			if len(m.Content) > MaxModelToolResultChars+100 {
				t.Fatalf("replayed tool result = %d bytes, must stay inside the model-visible window + fence", len(m.Content))
			}
			return
		}
	}
	_ = found
	t.Fatal("replayed history lost the tool result entirely")
}

// TestProviderMessages_ReplayKeepsTailWithinPersistedRecord pins the boundary
// between the two truncators: 40 KiB fits inside the 48 KiB persisted cap, so
// the persisted record must keep the full body AND the replay must not lose
// its tail. Baseline: replay window is head-only.
func TestProviderMessages_ReplayKeepsTailWithinPersistedRecord(t *testing.T) {
	chdirTemp(t)
	const tailSentinel = "PERSIST_REPLAY_TAIL_OK"
	content := replayToolContent(40_000, tailSentinel)

	sess := New()
	sess.AddToolResult("c2", "shell", content)
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The persisted record keeps everything (parity pin).
	persisted := loaded.Messages[len(loaded.Messages)-1].Content
	if len(persisted) != len(content) {
		t.Fatalf("persisted record lost bytes: persisted %d, want %d", len(persisted), len(content))
	}
	_ = tailSentinel

	for _, m := range loaded.ProviderMessages() {
		if string(m.Role) == "tool" && m.ToolCallID == "c2" {
			if !strings.Contains(m.Content, tailSentinel) {
				t.Fatalf("replay of an in-cap persisted record lost the tail window:\n%s", m.Content[len(m.Content)-400:])
			}
			if len(m.Content) > MaxModelToolResultChars+100 {
				t.Fatalf("replayed len = %d, want inside the window + fence", len(m.Content))
			}
			return
		}
	}
	t.Fatal("replayed history lost the tool result entirely")
}

// TestProviderMessages_ReplayShortToolResultUnchangedFenced is the parity
// floor: a short tool result replays byte-identical to the run-loop's
// truncate → scrub → fence pipeline and must keep doing so after the redesign.
func TestProviderMessages_ReplayShortToolResultUnchangedFenced(t *testing.T) {
	chdirTemp(t)
	const content = "delta-v report: small, complete, nothing truncated"

	sess := New()
	sess.AddToolResult("c3", "shell", content)
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := FenceToolResult("shell", TruncateModelToolResult(ScrubContent(content)))
	for _, m := range loaded.ProviderMessages() {
		if string(m.Role) == "tool" && m.ToolCallID == "c3" {
			if m.Content != want {
				t.Fatalf("short tool result must replay exactly the run-loop pipeline output:\nreplayed:\n%s\nwant:\n%s", m.Content, want)
			}
			return
		}
	}
	t.Fatal("replayed history lost the tool result entirely")
}

// TestProviderMessages_ReplayKeepsFenceEscapeAndTail pins that the fence
// escape survives replay (parity) AND that a tail-precious result stays
// tail-complete after the redesign (RED at baseline).
func TestProviderMessages_ReplayKeepsFenceEscapeAndTail(t *testing.T) {
	chdirTemp(t)
	const tailSentinel = "TAILESCAPE_OK_55"
	// 8452 bytes; "</tool_result>" sits mid-content so it lands inside the
	// head window and must be escaped by the fence on replay.
	content := strings.Repeat("B", 4400) + "MID</tool_result>MID" + strings.Repeat("C", 4016) + tailSentinel

	sess := New()
	sess.AddToolResult("c4", "search_code", content)
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, m := range loaded.ProviderMessages() {
		if string(m.Role) == "tool" && m.ToolCallID == "c4" {
			if !strings.Contains(m.Content, "<\\/tool_result>") {
				t.Fatalf("fence escape must survive replay")
			}
			if !strings.Contains(m.Content, tailSentinel) {
				t.Fatalf("replay heads-only: the escape check passed but the tail sentinel was lost:\n…%s", m.Content[len(m.Content)-300:])
			}
			return
		}
	}
	t.Fatal("replayed history lost the tool result entirely")
}

// TestProviderMessages_ReplayHugePersistedRecordStaysUnderWindow pins the
// strict replay bound for content larger than the 48 KiB persisted cap: the
// replay window (after the redesigned truncate) plus the fence must stay
// inside ~6 KiB. Baseline: head-only cut overflows the window past 6 KiB.
func TestProviderMessages_ReplayHugePersistedRecordStaysUnderWindow(t *testing.T) {
	chdirTemp(t)
	content := strings.Repeat("h", 60_000)
	sess := New()
	sess.AddToolResult("c5", "shell", content)
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, m := range loaded.ProviderMessages() {
		if string(m.Role) == "tool" && m.ToolCallID == "c5" {
			// Fence adds <= ~50 bytes around a <=6000-byte window.
			if len(m.Content) > MaxModelToolResultChars+100 {
				t.Fatalf("replayed len = %d; designed window keeps the marker inside the budget, never after it", len(m.Content))
			}
			return
		}
	}
	t.Fatal("replayed history lost the tool result entirely")
}
