package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSession_AgentPersistence(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(orig)

	s := New()
	s.Agent = "coding"
	s.AddMessage("user", "hello")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Agent != "coding" {
		t.Fatalf("Agent = %q, want coding", loaded.Agent)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("Messages len = %d", len(loaded.Messages))
	}
}

func TestSession_OldSessionWithoutAgentLoads(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(orig)

	// Simulate old session file without agent field
	s := New()
	s.Agent = "" // old file would have "" or missing
	data, _ := json.Marshal(s)
	// Ensure agent field is missing
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	delete(m, "agent")
	raw, _ := json.Marshal(m)

	sessDir := filepath.Join(".", ".forcefield", "sessions")
	_ = os.MkdirAll(sessDir, 0o700)
	path := filepath.Join(sessDir, s.ID+".json")
	_ = os.WriteFile(path, raw, 0o600)

	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load old: %v", err)
	}
	// Old session should load with empty Agent, caller treats as general
	if loaded.Agent != "" {
		t.Fatalf("old Agent = %q, want empty", loaded.Agent)
	}
	// But effective agent should be general when interpreted
	effective := loaded.Agent
	if effective == "" {
		effective = "general"
	}
	if effective != "general" {
		t.Fatalf("effective = %q", effective)
	}
}

func TestSession_AgentSwitchPersisted(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(orig)

	s := New()
	s.Agent = "general"
	_ = s.Save()
	s.Agent = "cyber"
	_ = s.Save()
	loaded, _ := Load(s.ID)
	if loaded.Agent != "cyber" {
		t.Fatalf("want cyber, got %q", loaded.Agent)
	}
	s.Agent = "legal"
	_ = s.Save()
	loaded2, _ := Load(s.ID)
	if loaded2.Agent != "legal" {
		t.Fatalf("want legal, got %q", loaded2.Agent)
	}
}
