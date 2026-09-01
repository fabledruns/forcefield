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
