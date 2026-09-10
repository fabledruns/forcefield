package session

import (
	"fmt"
	"testing"
)

func TestSessionCompaction_BoundsGrowth(t *testing.T) {
	s := New()
	// Add more than maxSessionMessages (1000) messages
	for i := 0; i < 1200; i++ {
		s.AddMessage("user", fmt.Sprintf("msg %d", i))
	}
	if len(s.Messages) > maxSessionMessages {
		t.Fatalf("session should be compacted to <= %d, got %d", maxSessionMessages, len(s.Messages))
	}
	// First message should be preserved (goal) if it was user
	if s.Messages[0].Content != "msg 0" {
		t.Errorf("first message should be preserved as goal, got %q", s.Messages[0].Content)
	}
	// Last message should be the most recent
	last := s.Messages[len(s.Messages)-1]
	if last.Content != "msg 1199" {
		t.Errorf("last message should be most recent, got %q", last.Content)
	}
}

func TestSessionCompaction_ProviderMessagesStillFenced(t *testing.T) {
	s := New()
	for i := 0; i < 1100; i++ {
		s.AddToolResult(fmt.Sprintf("c%d", i), "shell", fmt.Sprintf("output %d", i))
	}
	msgs := s.ProviderMessages()
	if len(msgs) > maxSessionMessages {
		t.Fatalf("ProviderMessages should be bounded, got %d", len(msgs))
	}
}

// TestSessionCompaction_ResumeAfterCompaction pins the resume path for a
// long-running session: after compaction, save/load round-trips and the
// replayed history keeps the original goal plus recent turns, with the
// observability marker skipped (never sent to the provider).
func TestSessionCompaction_ResumeAfterCompaction(t *testing.T) {
	_ = chdirTemp(t)
	s := New()
	s.AddMessage("user", "original goal")
	for i := 0; i < maxSessionMessages+100; i++ {
		s.AddMessage("user", fmt.Sprintf("turn %d", i))
		s.AddMessage("assistant", fmt.Sprintf("reply %d", i))
	}
	if s.Compacted == 0 {
		t.Fatal("expected compaction to drop messages")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	resumed, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if resumed.Compacted != s.Compacted {
		t.Errorf("Compacted = %d after reload, want %d", resumed.Compacted, s.Compacted)
	}
	replay := resumed.ProviderMessages()
	if len(replay) == 0 || replay[0].Content != "original goal" {
		t.Errorf("replay lost the original goal: %+v", replay[:1])
	}
	last := replay[len(replay)-1]
	if last.Content != fmt.Sprintf("reply %d", maxSessionMessages+99) {
		t.Errorf("replay lost the most recent turn: %q", last.Content)
	}
	for _, m := range replay {
		if string(m.Role) == "system" {
			t.Errorf("replay leaked a system marker to the provider: %+v", m)
		}
	}
}
