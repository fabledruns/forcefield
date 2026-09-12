package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"forcefield/internal/providers"
)

func TestPersistedArgumentsCapBoundsOversizedString(t *testing.T) {
	s := New()
	huge := strings.Repeat("z", maxPersistedArgStringBytes+50000)
	s.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: "args-1", Name: "shell", Arguments: map[string]any{"command": huge}},
	})
	if len(s.Messages) != 1 || len(s.Messages[0].ToolCalls) != 1 {
		t.Fatalf("messages = %+v, want one assistant batch with one call", s.Messages)
	}
	got, ok := s.Messages[0].ToolCalls[0].Arguments["command"].(string)
	if !ok {
		t.Fatalf("command arg = %T, want string", s.Messages[0].ToolCalls[0].Arguments["command"])
	}
	if len(got) > maxPersistedArgStringBytes+256 {
		t.Fatalf("persisted arg len = %d, want bounded near %d", len(got), maxPersistedArgStringBytes)
	}
	if !strings.Contains(got, "persisted argument truncated") {
		t.Error("truncation marker missing from capped argument")
	}
	if !utf8.ValidString(got) {
		t.Error("capped argument is not valid UTF-8")
	}
}

func TestPersistedArgumentsCapSmallValuesUnchanged(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: "args-2", Name: "shell", Arguments: map[string]any{"command": "echo hi", "retries": 3, "ok": true}},
	})
	args := s.Messages[0].ToolCalls[0].Arguments
	if args["command"] != "echo hi" || args["retries"] != 3 || args["ok"] != true {
		t.Errorf("small args changed: %+v", args)
	}
}

func TestPersistedArgumentsCapNestedAndUTF8Safe(t *testing.T) {
	s := New()
	huge := strings.Repeat("世", (maxPersistedArgStringBytes/3)+100)
	s.AddAssistantToolCalls("", []providers.ToolCall{{
		ID:   "args-3",
		Name: "shell",
		Arguments: map[string]any{
			"env":   map[string]any{"blob": huge},
			"items": []any{"fine", huge},
			"tags":  []string{"fine", huge},
		},
	}})
	args := s.Messages[0].ToolCalls[0].Arguments
	for _, v := range []any{
		args["env"].(map[string]any)["blob"],
		args["items"].([]any)[1],
		args["tags"].([]string)[1],
	} {
		str := v.(string)
		if !utf8.ValidString(str) {
			t.Fatal("capped nested argument is not valid UTF-8")
		}
		if !strings.Contains(str, "persisted argument truncated") {
			t.Errorf("nested value missing marker: %.60q", str)
		}
	}
	if args["items"].([]any)[0] != "fine" || args["tags"].([]string)[0] != "fine" {
		t.Error("small nested values must pass through unchanged")
	}
}

func TestPersistedArgumentsCapDoesNotMutateCaller(t *testing.T) {
	s := New()
	huge := strings.Repeat("q", maxPersistedArgStringBytes+100)
	orig := map[string]any{"command": huge, "nested": map[string]any{"v": huge}}
	s.AddAssistantToolCalls("", []providers.ToolCall{{ID: "args-4", Name: "shell", Arguments: orig}})
	if len(orig["command"].(string)) != maxPersistedArgStringBytes+100 {
		t.Error("caller's top-level argument was mutated")
	}
	if len(orig["nested"].(map[string]any)["v"].(string)) != maxPersistedArgStringBytes+100 {
		t.Error("caller's nested argument was mutated")
	}
}

func TestPersistedArgumentsCapStillScrubSecrets(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{{
		ID:   "args-5",
		Name: "shell",
		Arguments: map[string]any{
			"command": "deploy --token sk-ant-secret-abcdef1234567890",
		},
	}})
	got := s.Messages[0].ToolCalls[0].Arguments["command"].(string)
	if strings.Contains(got, "sk-ant-secret-abcdef1234567890") {
		t.Errorf("secret survived persistence sanitization: %q", got)
	}
}

func TestPersistedArgumentsCapPendingCalls(t *testing.T) {
	s := New()
	huge := strings.Repeat("p", maxPersistedArgStringBytes+1000)
	s.AddPendingCall(providers.ToolCall{ID: "args-6", Name: "shell", Arguments: map[string]any{"command": huge}})
	if len(s.Turn.Pending) != 1 {
		t.Fatalf("pending = %+v, want one call", s.Turn.Pending)
	}
	got, ok := s.Turn.Pending[0].Arguments["command"].(string)
	if !ok || len(got) > maxPersistedArgStringBytes+256 {
		t.Fatalf("pending arg unbounded: %d bytes", len(got))
	}
	if !strings.Contains(got, "persisted argument truncated") {
		t.Error("pending arg missing truncation marker")
	}
}

func TestPersistedArgumentsCapSurvivesSaveLoad(t *testing.T) {
	_ = chdirTemp(t)
	s := New()
	huge := strings.Repeat("w", maxPersistedArgStringBytes+20000)
	s.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: "args-7", Name: "shell", Arguments: map[string]any{"command": huge}},
	})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(".forcefield", "sessions", s.ID+".json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(raw) > maxPersistedArgStringBytes+4096 {
		t.Fatalf("session file = %d bytes for one capped call, want bounded", len(raw))
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := loaded.Messages[0].ToolCalls[0].Arguments["command"].(string)
	if !strings.Contains(got, "persisted argument truncated") {
		t.Error("reloaded arg lost its truncation marker")
	}
	var decoded Session
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
}

func TestSameTurnDuplicateIDsKeepFirst(t *testing.T) {
	s := New()
	s.AddAssistantToolCalls("", []providers.ToolCall{
		{ID: "st-1", Name: "shell", Arguments: map[string]any{"command": "first"}},
		{ID: "st-1", Name: "shell", Arguments: map[string]any{"command": "second"}},
		{ID: "st-2", Name: "shell"},
		{ID: "", Name: "shell"},
		{ID: "", Name: "shell"},
	})
	if len(s.Messages) != 1 {
		t.Fatalf("messages = %d, want 1 batch", len(s.Messages))
	}
	got := s.Messages[0].ToolCalls
	if len(got) != 4 {
		t.Fatalf("batch = %d calls, want 4 (st-1 once, st-2, two empty-ID)", len(got))
	}
	if got[0].ID != "st-1" || got[0].Arguments["command"] != "first" {
		t.Errorf("first occurrence must win: %+v", got[0])
	}
	if got[1].ID != "st-2" || got[2].ID != "" || got[3].ID != "" {
		t.Errorf("unexpected batch shape: %+v", got)
	}
}

func TestAppendToolCallSanitizesArguments(t *testing.T) {
	s := New()
	huge := strings.Repeat("v", maxPersistedArgStringBytes+5000)
	s.AppendToolCallToLastAssistant(providers.ToolCall{
		ID:   "san-1",
		Name: "shell",
		Arguments: map[string]any{
			"command": huge,
			"leak":    "key sk-ant-secret-abcdef1234567890 here",
		},
	}, "")
	if len(s.Messages) != 1 || len(s.Messages[0].ToolCalls) != 1 {
		t.Fatalf("messages = %+v, want one batch with one call", s.Messages)
	}
	args := s.Messages[0].ToolCalls[0].Arguments
	cmd := args["command"].(string)
	if len(cmd) > maxPersistedArgStringBytes+256 {
		t.Errorf("incremental arg unbounded: %d bytes", len(cmd))
	}
	if !strings.Contains(cmd, "persisted argument truncated") {
		t.Error("incremental arg missing truncation marker")
	}
	if leak := args["leak"].(string); strings.Contains(leak, "sk-ant-secret-abcdef1234567890") {
		t.Errorf("incremental arg secret not scrubbed: %q", leak)
	}
}

func TestAppendToolCallSanitizeDoesNotMutateCaller(t *testing.T) {
	s := New()
	huge := strings.Repeat("v", maxPersistedArgStringBytes+100)
	orig := map[string]any{"command": huge}
	s.AppendToolCallToLastAssistant(providers.ToolCall{ID: "san-2", Name: "shell", Arguments: orig}, "")
	if len(orig["command"].(string)) != maxPersistedArgStringBytes+100 {
		t.Error("caller's argument map was mutated by the incremental path")
	}
}
