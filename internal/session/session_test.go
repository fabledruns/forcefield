package session

import (
	"os"
	"strings"
	"testing"
)

func TestNewlinesRoundTripThroughSaveAndLoad(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".forcefield") })

	s := New()
	text := "line one\nline two\n\nline four ✓"
	s.AddMessage("user", text)

	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("loaded %d messages, want 1", len(loaded.Messages))
	}

	got := loaded.Messages[0].Content
	if got != text {
		t.Errorf("round-tripped content = %q, want %q", got, text)
	}
	if strings.Contains(got, "\\n") {
		t.Errorf("content contains literal backslash-n: %q", got)
	}

	pm := loaded.ProviderMessages()
	if len(pm) != 1 || pm[0].Content != text {
		t.Errorf("ProviderMessages() = %+v, want verbatim content", pm)
	}
}
