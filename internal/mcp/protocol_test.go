package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSupportedVersionsAccepted(t *testing.T) {
	for _, v := range SupportedVersions {
		if !IsSupported(v) {
			t.Errorf("IsSupported(%q) = false, want true", v)
		}
		if got, err := Negotiate(v); err != nil || got != v {
			t.Errorf("Negotiate(%q) = %q, %v; want %q, nil", v, got, err, v)
		}
	}
}

func TestUnsupportedVersionRejected(t *testing.T) {
	for _, v := range []string{"", "1.0", "2.0", "2023-01-01", "2025-11-26", "latest"} {
		if IsSupported(v) {
			t.Errorf("IsSupported(%q) = true, want false", v)
		}
		if _, err := Negotiate(v); !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("Negotiate(%q) err = %v, want ErrUnsupportedVersion", v, err)
		}
	}
}

func TestFutureVersionRejected(t *testing.T) {
	if _, err := Negotiate("2030-01-01"); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("Negotiate(future) err = %v, want ErrUnsupportedVersion", err)
	}
	if _, err := Negotiate("2026-99-99"); !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("Negotiate(future-ish) err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestMalformedVersionRejected(t *testing.T) {
	for _, v := range []string{" 2025-11-25", "2025-11-25 ", "2025-11-25\n", "v2025-11-25", "2025/11/25"} {
		if IsSupported(v) {
			t.Errorf("IsSupported(%q) = true, want exact-match false", v)
		}
		if _, err := Negotiate(v); !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("Negotiate(%q) err = %v, want ErrUnsupportedVersion", v, err)
		}
	}
}

func TestVersionErrorIsBounded(t *testing.T) {
	huge := strings.Repeat("9", MaxErrorDetailRunes+100)
	_, err := Negotiate(huge)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Negotiate(huge) err = %v, want ErrUnsupportedVersion", err)
	}
	if len([]rune(err.Error())) > MaxErrorDetailRunes+256 {
		t.Errorf("version error of %d runes is unbounded", len([]rune(err.Error())))
	}
}

func TestValidateInitializeResult(t *testing.T) {
	res := InitializeResult{
		ProtocolVersion: "2025-11-25",
		Capabilities:    json.RawMessage(`{"tools":{}}`),
		ServerInfo:      Implementation{Name: "test"},
	}
	if got, err := ValidateInitializeResult(res); err != nil || got != "2025-11-25" {
		t.Errorf("ValidateInitializeResult = %q, %v; want 2025-11-25, nil", got, err)
	}
}

func TestValidateInitializeResultRejects(t *testing.T) {
	cases := []InitializeResult{
		{ProtocolVersion: "1999-01-01", Capabilities: json.RawMessage(`{"tools":{}}`)},
		{ProtocolVersion: "2025-11-25", Capabilities: json.RawMessage(`{"resources":{}}`)},
		{ProtocolVersion: "2025-11-25", Capabilities: json.RawMessage(`{}`)},
		{ProtocolVersion: "2025-11-25", Capabilities: nil},
		{ProtocolVersion: "", Capabilities: json.RawMessage(`{"tools":{}}`)},
	}
	for i, res := range cases {
		if _, err := ValidateInitializeResult(res); err == nil {
			t.Errorf("case %d: ValidateInitializeResult = nil error, want failure", i)
		}
	}
}

func TestHasToolsCapability(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"tools":{}}`:                true,
		`{"tools":{},"resources":{}}`: true,
		`{"tools":null}`:              true,
		`{"resources":{}}`:            false,
		`{}`:                          false,
		`[]`:                          false,
		`not json`:                    false,
		``:                            false,
	} {
		if got := HasToolsCapability(json.RawMessage(raw)); got != want {
			t.Errorf("HasToolsCapability(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestValidateListPage(t *testing.T) {
	if err := ValidateListPage(ListToolsResult{Tools: []json.RawMessage{}}); err != nil {
		t.Errorf("empty tools array err = %v, want nil", err)
	}
	if err := ValidateListPage(ListToolsResult{Tools: nil}); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("nil tools err = %v, want ErrInvalidMessage", err)
	}
}

func TestValidateCallParams(t *testing.T) {
	if err := ValidateCallParams(CallToolParams{Name: "read_file"}); err != nil {
		t.Errorf("valid call params err = %v, want nil", err)
	}
	if err := ValidateCallParams(CallToolParams{}); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("empty name err = %v, want ErrInvalidMessage", err)
	}
	if err := ValidateCallParams(CallToolParams{Name: strings.Repeat("x", MaxRemoteToolNameLen+1)}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over-long name err = %v, want ErrTooLarge", err)
	}
	atCap := CallToolParams{Name: strings.Repeat("x", MaxRemoteToolNameLen)}
	if err := ValidateCallParams(atCap); err != nil {
		t.Errorf("boundary name err = %v, want nil", err)
	}
}

func TestMethodConstants(t *testing.T) {
	for _, m := range []string{MethodInitialize, MethodInitialized, MethodListTools, MethodCallTool} {
		if strings.TrimSpace(m) == "" {
			t.Errorf("method constant is blank")
		}
	}
	if ClientVersion == "" || !IsSupported(ClientVersion) {
		t.Errorf("ClientVersion %q must be an allowlisted version", ClientVersion)
	}
}
