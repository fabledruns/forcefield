package session

import (
	"encoding/json"
	"os"
	"testing"

	"forcefield/internal/providers"
)

func TestProviderMessagesPreservesToolCalls(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".forcefield") })

	s := New()
	s.AddMessage("user", "use a tool")
	s.AddAssistantToolCalls("checking", []providers.ToolCall{
		{ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "a.txt"}},
		{ID: "call-2", Name: "shell", Arguments: map[string]any{"cmd": "ls"}},
	})
	s.AddToolResult("call-1", "read_file", "file contents")
	s.AddToolResult("call-2", "shell", "output")

	msgs := s.ProviderMessages()
	if len(msgs) != 4 {
		t.Fatalf("ProviderMessages len = %d, want 4", len(msgs))
	}
	if len(msgs[1].ToolCalls) != 2 || msgs[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("assistant ToolCalls = %+v, want 2 with call-1", msgs[1].ToolCalls)
	}
	if msgs[2].ToolCallID != "call-1" || msgs[2].Name != "read_file" {
		t.Fatalf("tool result 1 = %+v, want call-1/read_file", msgs[2])
	}
	if msgs[3].ToolCallID != "call-2" {
		t.Fatalf("tool result 2 = %+v, want call-2", msgs[3])
	}
}

func TestSaveLoadRoundTripWithToolCalls(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".forcefield") })

	s := New()
	s.AddAssistantToolCalls("thinking", []providers.ToolCall{
		{ID: "c1", Name: "echo", Arguments: map[string]any{"value": "x"}},
	})
	s.AddToolResult("c1", "echo", "x")

	if err := s.Save(); err != nil {
		t.Fatalf("Save = %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("loaded messages = %d, want 2", len(loaded.Messages))
	}
	if len(loaded.Messages[0].ToolCalls) != 1 || loaded.Messages[0].ToolCalls[0].Name != "echo" {
		t.Fatalf("loaded toolCalls = %+v", loaded.Messages[0].ToolCalls)
	}
	msgs := loaded.ProviderMessages()
	if len(msgs[1].ToolCallID) == 0 {
		t.Fatal("ProviderMessages after load lost ToolCallID")
	}
}

func TestLoadOldSessionWithoutToolFields(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".forcefield") })

	// Simulate old file with only role/content/time
	old := `{"id":"old-id","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","messages":[{"role":"user","content":"hello","time":"2026-01-01T00:00:00Z"},{"role":"assistant","content":"hi","time":"2026-01-01T00:00:00Z"}]}`
	dir := ".forcefield/sessions"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := dir + "/old-id.json"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load("old-id")
	if err != nil {
		t.Fatalf("Load old = %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(loaded.Messages))
	}
	msgs := loaded.ProviderMessages()
	if len(msgs) != 2 || msgs[0].Content != "hello" {
		t.Fatalf("ProviderMessages = %+v", msgs)
	}
	// Ensure new fields are empty but not error
	if len(loaded.Messages[0].ToolCalls) != 0 || loaded.Messages[0].ToolCallID != "" {
		t.Fatalf("old message tool fields not empty: %+v", loaded.Messages[0])
	}
	// Save should produce new fields with omitempty (not break)
	if err := loaded.Save(); err != nil {
		t.Fatalf("Save after old load = %v", err)
	}
	data, _ := os.ReadFile(path)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json unmarshal saved = %v", err)
	}
}

func TestAppendToolCallToLastAssistantCoalesces(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".forcefield") })
	s := New()
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "a1", Name: "toolA"}, "first")
	if len(s.Messages) != 1 || len(s.Messages[0].ToolCalls) != 1 {
		t.Fatalf("after first append = %+v", s.Messages)
	}
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "a2", Name: "toolB"}, "")
	if len(s.Messages) != 1 || len(s.Messages[0].ToolCalls) != 2 {
		t.Fatalf("after second append should coalesce into one batch, got %+v", s.Messages)
	}
	if s.Messages[0].ToolCalls[1].ID != "a2" {
		t.Fatalf("second call not appended")
	}
	// After a tool result, next assistant call should be new message
	s.AddToolResult("a1", "toolA", "result")
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "b1", Name: "toolC"}, "")
	if len(s.Messages) != 3 {
		t.Fatalf("after tool result, new assistant should be separate message, got %d: %+v", len(s.Messages), s.Messages)
	}
}
