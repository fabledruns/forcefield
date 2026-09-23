package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeSegmentKeepsLegal(t *testing.T) {
	for _, s := range []string{"read_file", "fs-read", "A1_-b", "x"} {
		if got, err := SanitizeSegment(s); err != nil || got != s {
			t.Errorf("SanitizeSegment(%q) = %q, %v; want unchanged", s, got, err)
		}
	}
}

func TestSanitizeSegmentMapsIllegal(t *testing.T) {
	got, err := SanitizeSegment("fs.read file")
	if err != nil {
		t.Fatalf("SanitizeSegment error = %v", err)
	}
	if got != "fs_read_file" {
		t.Errorf("SanitizeSegment = %q, want fs_read_file", got)
	}
}

func TestSanitizeSegmentRejects(t *testing.T) {
	if _, err := SanitizeSegment(""); !errors.Is(err, ErrInvalidTool) {
		t.Errorf("empty err = %v, want ErrInvalidTool", err)
	}
	if _, err := SanitizeSegment(strings.Repeat("a", MaxRemoteToolNameLen+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over-long err = %v, want ErrTooLarge", err)
	}
	atCap, err := SanitizeSegment(strings.Repeat("a", MaxRemoteToolNameLen))
	if err != nil || len([]rune(atCap)) != MaxRemoteToolNameLen {
		t.Errorf("boundary err = %v len = %d, want nil and %d", err, len([]rune(atCap)), MaxRemoteToolNameLen)
	}
}

func TestQualifiedName(t *testing.T) {
	got, err := QualifiedName("filesystem", "read_file")
	if err != nil || got != "mcp__filesystem__read_file" {
		t.Errorf("QualifiedName = %q, %v; want mcp__filesystem__read_file", got, err)
	}
	if _, err := QualifiedName("bad key!", "tool"); err == nil {
		t.Error("bad server key accepted, want error")
	}
	if _, err := QualifiedName("srv", ""); err == nil {
		t.Error("empty remote name accepted, want error")
	}
	// A long-but-legal pair that overflows the qualified budget is
	// rejected, never truncated into ambiguity.
	longServer := strings.Repeat("s", MaxServerKeyLen)
	if _, err := QualifiedName(longServer, strings.Repeat("t", MaxRemoteToolNameLen)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("overflowing qualified name err = %v, want ErrTooLarge", err)
	}
}

func TestIsQualifiedToolName(t *testing.T) {
	for _, name := range []string{
		"mcp__filesystem__read_file",
		"mcp__a__b",
		"mcp__srv-1__tool_2",
		// Segments may themselves contain underscores, so names with
		// extra separators still match. Splitting ambiguity never
		// matters: later layers compare qualified names as whole
		// strings and fail closed on any cross-server collision.
		"mcp__srv__tool__extra",
		"mcp_____tool", // server "_" + tool "tool"
	} {
		if !IsQualifiedToolName(name) {
			t.Errorf("IsQualifiedToolName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"",
		"shell",
		"read_file",
		"mcp__onlyone",
		"mcp__srv__",
		"MCP__srv__tool",
		"mcp__srv__has space",
		strings.Repeat("x", MaxQualifiedToolNameLen+1),
	} {
		if IsQualifiedToolName(name) {
			t.Errorf("IsQualifiedToolName(%q) = true, want false", name)
		}
	}
}

func TestTruncateDescription(t *testing.T) {
	short := "read a file"
	if got, cut := TruncateDescription(short); cut || got != short {
		t.Errorf("short description cut = %v, want unchanged", cut)
	}
	exact := strings.Repeat("a", MaxDescriptionBytes)
	if got, cut := TruncateDescription(exact); cut || got != exact {
		t.Errorf("boundary description cut = %v, want unchanged", cut)
	}
	over := strings.Repeat("b", MaxDescriptionBytes+1)
	got, cut := TruncateDescription(over)
	if !cut {
		t.Fatal("over-long description not cut")
	}
	if !strings.HasSuffix(got, TruncationMarker) {
		t.Errorf("truncated description missing marker: %q", got[len(got)-20:])
	}
	// Rune safety: a multibyte tail must never split.
	multi := strings.Repeat("é", MaxDescriptionBytes) + "x"
	got, cut = TruncateDescription(multi)
	if !cut {
		t.Fatal("multibyte over-long description not cut")
	}
	if !utf8.ValidString(got) {
		t.Error("truncated description is not valid UTF-8")
	}
}

func TestParseToolEntry(t *testing.T) {
	tool, err := ParseToolEntry(json.RawMessage(`{"name":"read_file","description":"reads","inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}}`))
	if err != nil {
		t.Fatalf("ParseToolEntry error = %v", err)
	}
	if tool.Name != "read_file" || tool.Description != "reads" {
		t.Errorf("tool = %+v, want name/description", tool)
	}
	if tool.InputSchema["type"] != "object" {
		t.Errorf("schema = %v", tool.InputSchema)
	}
	// Absent optionals stay absent.
	tool, err = ParseToolEntry(json.RawMessage(`{"name":"ping"}`))
	if err != nil || tool.Description != "" || tool.InputSchema != nil {
		t.Errorf("minimal entry = %+v, %v; want bare name", tool, err)
	}
	// Malformed entries fail safely.
	for _, raw := range []string{
		``,
		`{}`,
		`{"name":""}`,
		`{"name":42}`,
		`{"name":"t","description":42}`,
		`{"name":"t","inputSchema":[]}`,
		`not json`,
	} {
		if _, err := ParseToolEntry(json.RawMessage(raw)); !errors.Is(err, ErrInvalidTool) {
			t.Errorf("ParseToolEntry(%q) err = %v, want ErrInvalidTool", raw, err)
		}
	}
}

func TestValidateRemoteToolBounds(t *testing.T) {
	valid := RemoteTool{Name: "read_file", Description: "ok",
		InputSchema: map[string]any{"type": "object"}}
	if err := ValidateRemoteTool(valid); err != nil {
		t.Errorf("valid tool err = %v, want nil", err)
	}
	// Over-long name rejected.
	bad := RemoteTool{Name: strings.Repeat("n", MaxRemoteToolNameLen+1)}
	if err := ValidateRemoteTool(bad); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over-long name err = %v, want ErrTooLarge", err)
	}
	// Oversized schema rejected (never truncated).
	huge := map[string]any{"type": "object", "blob": strings.Repeat("z", MaxSchemaBytes)}
	if err := ValidateRemoteTool(RemoteTool{Name: "t", InputSchema: huge}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized schema err = %v, want ErrTooLarge", err)
	}
	// Exact serialized boundary accepted: build a schema just under the cap.
	filler := strings.Repeat("f", MaxSchemaBytes-64)
	edge := map[string]any{"type": "object", "note": filler}
	raw, _ := json.Marshal(edge)
	if len(raw) > MaxSchemaBytes {
		t.Skip("test setup exceeds schema cap; adjust filler")
	}
	if err := ValidateRemoteTool(RemoteTool{Name: "t", InputSchema: edge}); err != nil {
		t.Errorf("boundary schema err = %v, want nil", err)
	}
	// Non-object properties rejected.
	badProps := map[string]any{"type": "object", "properties": []any{}}
	if err := ValidateRemoteTool(RemoteTool{Name: "t", InputSchema: badProps}); !errors.Is(err, ErrInvalidTool) {
		t.Errorf("bad properties err = %v, want ErrInvalidTool", err)
	}
	// Too many properties rejected.
	many := map[string]any{}
	for i := 0; i < MaxSchemaProperties+1; i++ {
		many[string(rune('a'+i%26))+strings.Repeat("x", i/26+1)] = map[string]any{"type": "string"}
	}
	if err := ValidateRemoteTool(RemoteTool{Name: "t", InputSchema: map[string]any{"properties": many}}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too many properties err = %v, want ErrTooLarge", err)
	}
	// Boundary property count accepted.
	exact := map[string]any{}
	for i := 0; i < MaxSchemaProperties; i++ {
		exact["p"+strings.Repeat("q", i)] = map[string]any{"type": "string"}
	}
	if err := ValidateRemoteTool(RemoteTool{Name: "t", InputSchema: map[string]any{"properties": exact}}); err != nil {
		t.Errorf("boundary properties err = %v, want nil", err)
	}
	// Over-deep nesting rejected.
	deep := map[string]any{"type": "object"}
	cur := deep
	for i := 0; i < MaxSchemaDepth+1; i++ {
		next := map[string]any{"type": "object"}
		cur["properties"] = map[string]any{"n": next}
		cur = next
	}
	if err := ValidateRemoteTool(RemoteTool{Name: "t", InputSchema: deep}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("deep schema err = %v, want ErrTooLarge", err)
	}
	// Oversized enum rejected; boundary accepted.
	bigEnum := []any{}
	for i := 0; i < MaxSchemaEnumEntries+1; i++ {
		bigEnum = append(bigEnum, "v")
	}
	if err := ValidateRemoteTool(RemoteTool{Name: "t", InputSchema: map[string]any{"enum": bigEnum}}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("big enum err = %v, want ErrTooLarge", err)
	}
	okEnum := []any{}
	for i := 0; i < MaxSchemaEnumEntries; i++ {
		okEnum = append(okEnum, "v")
	}
	if err := ValidateRemoteTool(RemoteTool{Name: "t", InputSchema: map[string]any{"enum": okEnum}}); err != nil {
		t.Errorf("boundary enum err = %v, want nil", err)
	}
}

func TestSecretLookingKey(t *testing.T) {
	for _, k := range []string{"API_KEY", "api_key", "GITHUB_TOKEN", "my-secret", "SECRET_PATH", "key"} {
		if !SecretLookingKey(k) {
			t.Errorf("SecretLookingKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"PATH", "HOME", "command", "cwd", ""} {
		if SecretLookingKey(k) {
			t.Errorf("SecretLookingKey(%q) = true, want false", k)
		}
	}
}

func TestBoundedWarning(t *testing.T) {
	short := "tool skipped: bad schema"
	if got := BoundedWarning(short); got != short {
		t.Errorf("short warning changed: %q", got)
	}
	long := strings.Repeat("w", MaxErrorDetailRunes+100)
	got := BoundedWarning(long)
	if len([]rune(got)) > MaxErrorDetailRunes+len(TruncationMarker) {
		t.Errorf("warning of %d runes is unbounded", len([]rune(got)))
	}
}
