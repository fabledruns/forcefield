package tui

import (
	"strings"
	"testing"

	"forcefield/internal/session"
)

func TestSessionEntries_RendersToolHistory(t *testing.T) {
	sess := session.New()
	sess.AddMessage("user", "hello")
	sess.AddMessage("assistant", "I will check")
	sess.AddToolResult("call-1", "read_file", "file content here")
	sess.AddToolResult("call-2", "shell", "shell output")

	entries := sessionEntries(sess)
	// Should have user, assistant, plus 2 tool activity entries
	if len(entries) != 4 {
		t.Fatalf("entries len %d, want 4 (user, assistant, 2 tools)", len(entries))
	}
	if entries[0].Role != roleUser || entries[0].Content != "hello" {
		t.Errorf("first entry = %+v, want user hello", entries[0])
	}
	if entries[1].Role != roleAssistant {
		t.Errorf("second entry role %v, want assistant", entries[1].Role)
	}
	// Tool entries should be activity with Tool present
	for i := 2; i < 4; i++ {
		if entries[i].Role != roleActivity || entries[i].Tool == nil {
			t.Errorf("entry %d should be tool activity with Tool, got %+v", i, entries[i])
		}
		if entries[i].Tool.content == "" {
			t.Errorf("tool content empty for entry %d", i)
		}
	}
}

func TestSessionEntries_ToolContentPreserved(t *testing.T) {
	sess := session.New()
	sess.AddToolResult("c1", "shell", "line1\nline2")
	entries := sessionEntries(sess)
	if len(entries) != 1 {
		t.Fatalf("expected 1 tool entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Tool.content, "line1") {
		t.Errorf("tool content should contain original, got %q", entries[0].Tool.content)
	}
}

func TestSessionEntries_EmptyAssistantSkipped(t *testing.T) {
	sess := session.New()
	sess.AddMessage("assistant", "")
	entries := sessionEntries(sess)
	if len(entries) != 0 {
		t.Fatalf("empty assistant should produce no entry, got %d", len(entries))
	}
}
