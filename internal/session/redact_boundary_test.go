package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/providers"
)

// TestPendingCallArgumentsScrubbedOnDisk pins the P0.4 envelope fix:
// secrets in tool arguments (commands, file content) must not reach the
// session file, in pending records or in the replay batch.
func TestPendingCallArgumentsScrubbedOnDisk(t *testing.T) {
	dir := chdirTemp(t)
	_ = dir
	s := New()
	call := providers.ToolCall{
		ID:   "c1",
		Name: "shell",
		Arguments: map[string]any{
			"command": `curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc12345678901234567890" https://api.example.com`,
		},
	}
	s.AddAssistantToolCalls("", []providers.ToolCall{call})
	s.AddPendingCall(call)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(".forcefield", "sessions", s.ID+".json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "eyJhbGci") {
		t.Errorf("session file leaked bearer token:\n%.500s", raw)
	}
	var decoded Session
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, p := range decoded.Turn.Pending {
		if cmd, _ := p.Arguments["command"].(string); strings.Contains(cmd, "eyJhbGci") {
			t.Errorf("pending args leaked secret: %q", cmd)
		}
	}
	for _, m := range decoded.Messages {
		for _, tc := range m.ToolCalls {
			if cmd, _ := tc.Arguments["command"].(string); strings.Contains(cmd, "eyJhbGci") {
				t.Errorf("batch args leaked secret: %q", cmd)
			}
		}
	}
	// Replay to the provider is clean too.
	for _, m := range decoded.ProviderMessages() {
		for _, tc := range m.ToolCalls {
			if cmd, _ := tc.Arguments["command"].(string); strings.Contains(cmd, "eyJhbGci") {
				t.Errorf("replay args leaked secret: %q", cmd)
			}
		}
	}
}

// TestResolvePendingCallErrorScrubbed pins that execution errors carrying
// secrets are sanitized in the persisted record.
func TestResolvePendingCallErrorScrubbed(t *testing.T) {
	s := New()
	s.AddPendingCall(providers.ToolCall{ID: "c1", Name: "shell"})
	s.ResolvePendingCall("c1", CallFailed, "dial failed with key AKIAIOSFODNN7EXAMPLE")
	if got := s.Turn.Pending[0].Error; strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("pending error leaked AWS key: %q", got)
	}
}
