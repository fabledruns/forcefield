package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPersistedToolResultCapBoundsOversizedOutput(t *testing.T) {
	s := New()
	huge := strings.Repeat("x", maxPersistedToolResultBytes+100000)
	s.AddToolResult("big-1", "shell", huge)
	if len(s.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(s.Messages))
	}
	got := s.Messages[0].Content
	if len(got) > maxPersistedToolResultBytes+512 {
		t.Fatalf("persisted len = %d, want bounded near %d", len(got), maxPersistedToolResultBytes)
	}
	if !strings.Contains(got, "persisted tool output truncated") {
		t.Error("truncation marker missing from capped tool result")
	}
	if !utf8.ValidString(got) {
		t.Error("capped tool result is not valid UTF-8")
	}
	if strings.Contains(got, huge[len(got):]) && len(huge) > len(got) {
		// Tail beyond the cap must not be retained verbatim.
		if strings.Contains(got, huge[maxPersistedToolResultBytes+1000:]) {
			t.Error("persisted content retains bytes far beyond the cap")
		}
	}
}

func TestPersistedToolResultCapKeepsSmallOutputUnchanged(t *testing.T) {
	s := New()
	small := "short output ✓"
	s.AddToolResult("small-1", "shell", small)
	if got := s.Messages[0].Content; got != small {
		t.Errorf("small result changed: %q", got)
	}
}

func TestPersistedToolResultCapUTF8Safe(t *testing.T) {
	s := New()
	// Multi-byte boundary: cap must not split a rune.
	huge := strings.Repeat("世", (maxPersistedToolResultBytes/3)+100)
	s.AddToolResult("uni-1", "shell", huge)
	got := s.Messages[0].Content
	if !utf8.ValidString(got) {
		t.Fatal("capped multi-byte result is not valid UTF-8")
	}
	if !strings.Contains(got, "persisted tool output truncated") {
		t.Error("marker missing for multi-byte oversized result")
	}
}

func TestPersistedToolResultCapSurvivesSaveLoad(t *testing.T) {
	_ = chdirTemp(t)
	s := New()
	huge := strings.Repeat("y", maxPersistedToolResultBytes+50000)
	s.AddToolResult("rt-1", "shell", huge)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("loaded messages = %d, want 1", len(loaded.Messages))
	}
	if len(loaded.Messages[0].Content) > maxPersistedToolResultBytes+512 {
		t.Fatalf("reloaded len = %d, want bounded", len(loaded.Messages[0].Content))
	}
}

func TestPersistedCapDoesNotTouchAssistantText(t *testing.T) {
	s := New()
	long := strings.Repeat("a", maxPersistedToolResultBytes+1000)
	s.AddMessage("assistant", long)
	if got := s.Messages[0].Content; got != long {
		t.Error("assistant text must not be capped by the tool-result bound")
	}
}
