package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseValidRequest(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"abc"}}`))
	if err != nil {
		t.Fatalf("ParseMessage error = %v", err)
	}
	if msg.Kind != KindRequest {
		t.Errorf("Kind = %v, want request", msg.Kind)
	}
	if !msg.HasID || msg.ID != "1" {
		t.Errorf("ID = %q hasID = %v, want \"1\" true", msg.ID, msg.HasID)
	}
	if msg.Method != "tools/list" {
		t.Errorf("Method = %q, want tools/list", msg.Method)
	}
}

func TestParseValidStringIDRequest(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":"req-1","method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("ParseMessage error = %v", err)
	}
	if msg.Kind != KindRequest || msg.ID != "req-1" {
		t.Errorf("got kind %v id %q, want request req-1", msg.Kind, msg.ID)
	}
}

func TestParseValidResultResponse(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
	if err != nil {
		t.Fatalf("ParseMessage error = %v", err)
	}
	if msg.Kind != KindResponse || msg.ID != "2" {
		t.Errorf("got kind %v id %q, want response 2", msg.Kind, msg.ID)
	}
	if len(msg.Result) == 0 {
		t.Error("Result missing")
	}
	if msg.Error != nil {
		t.Errorf("Error = %+v, want nil", msg.Error)
	}
}

func TestParseValidErrorResponse(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`))
	if err != nil {
		t.Fatalf("ParseMessage error = %v", err)
	}
	if msg.Kind != KindResponse || msg.Error == nil {
		t.Fatalf("got kind %v err %+v, want response with error", msg.Kind, msg.Error)
	}
	if msg.Error.Code != -32601 || msg.Error.Message != "Method not found" {
		t.Errorf("Error = %+v, want code -32601", msg.Error)
	}
}

func TestParseValidNotification(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	if err != nil {
		t.Fatalf("ParseMessage error = %v", err)
	}
	if msg.Kind != KindNotification {
		t.Errorf("Kind = %v, want notification", msg.Kind)
	}
	if msg.HasID {
		t.Error("notification must not carry an id")
	}
	if msg.Method != "notifications/initialized" {
		t.Errorf("Method = %q", msg.Method)
	}
}

func TestParseNullIDIsNotification(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":null,"method":"notifications/progress"}`))
	if err != nil {
		t.Fatalf("ParseMessage error = %v", err)
	}
	if msg.Kind != KindNotification {
		t.Errorf("Kind = %v, want notification for null id", msg.Kind)
	}
}

func TestParseMalformedJSON(t *testing.T) {
	for _, raw := range []string{
		`{"jsonrpc":`,
		`not json at all`,
		`{"jsonrpc":"2.0","id":1,`,
		"\x00\x01\x02",
	} {
		if _, err := ParseMessage([]byte(raw)); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("ParseMessage(%q) err = %v, want ErrInvalidMessage", raw, err)
		}
	}
}

func TestParseVersionMismatch(t *testing.T) {
	for _, raw := range []string{
		`{"jsonrpc":"1.0","id":1,"method":"tools/list"}`,
		`{"id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.1","id":1,"result":{}}`,
	} {
		if _, err := ParseMessage([]byte(raw)); !errors.Is(err, ErrProtocol) {
			t.Errorf("ParseMessage(%q) err = %v, want ErrProtocol", raw, err)
		}
	}
}

func TestParseBatchArrayRejected(t *testing.T) {
	raw := `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`
	if _, err := ParseMessage([]byte(raw)); !errors.Is(err, ErrProtocol) {
		t.Errorf("ParseMessage(batch) err = %v, want ErrProtocol", err)
	}
	// Leading whitespace must not smuggle a batch past the check.
	raw = "  \n [{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}]"
	if _, err := ParseMessage([]byte(raw)); !errors.Is(err, ErrProtocol) {
		t.Errorf("ParseMessage(padded batch) err = %v, want ErrProtocol", err)
	}
}

func TestParseInvalidIDs(t *testing.T) {
	cases := []string{
		// Response without id.
		`{"jsonrpc":"2.0","result":{}}`,
		// Null response id.
		`{"jsonrpc":"2.0","id":null,"result":{}}`,
		// Boolean id.
		`{"jsonrpc":"2.0","id":true,"result":{}}`,
		// Object id.
		`{"jsonrpc":"2.0","id":{},"method":"tools/list"}`,
		// Array id.
		`{"jsonrpc":"2.0","id":[1],"result":{}}`,
		// Empty string id.
		`{"jsonrpc":"2.0","id":"","method":"tools/list"}`,
		// Fractional numeric id.
		`{"jsonrpc":"2.0","id":1.5,"result":{}}`,
	}
	for _, raw := range cases {
		if _, err := ParseMessage([]byte(raw)); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("ParseMessage(%q) err = %v, want ErrInvalidMessage", raw, err)
		}
	}
}

func TestParseOversizedMessage(t *testing.T) {
	big := make([]byte, MaxMessageBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	if _, err := ParseMessage(big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("ParseMessage(oversize) err = %v, want ErrTooLarge", err)
	}
	// Exact boundary is accepted by the size gate (shape parsing may still
	// reject it, but never with ErrTooLarge).
	atCap := append([]byte(`{"jsonrpc":"2.0","id":1,"method":"m","params":"`), make([]byte, MaxMessageBytes-len(`{"jsonrpc":"2.0","id":1,"method":"m","params":"`)-len(`"}`))...)
	atCap = append(atCap, []byte(`"}`)...)
	if len(atCap) != MaxMessageBytes {
		t.Fatalf("test setup: boundary frame is %d bytes, want %d", len(atCap), MaxMessageBytes)
	}
	if _, err := ParseMessage(atCap); errors.Is(err, ErrTooLarge) {
		t.Errorf("ParseMessage(boundary) err = %v, want no ErrTooLarge", err)
	}
}

func TestParseMalformedShapes(t *testing.T) {
	cases := []string{
		`{"jsonrpc":"2.0","id":1}`,
		`{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"x"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"m","result":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":42}`,
		`{"jsonrpc":"2.0","id":1,"method":""}`,
		`{"jsonrpc":"2.0","id":1,"error":{"message":"x"}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":1}}`,
		`{"jsonrpc":"2.0","id":1,"error":{}}`,
		`{}`,
		``,
		`   `,
	}
	for _, raw := range cases {
		_, err := ParseMessage([]byte(raw))
		if err == nil {
			t.Errorf("ParseMessage(%q) = nil error, want failure", raw)
		}
	}
}

func TestParseServerInitiatedRequestClassified(t *testing.T) {
	// v1 never answers server-initiated requests; the parser only needs to
	// classify them so later layers can reject them deterministically.
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":9,"method":"sampling/createMessage","params":{}}`))
	if err != nil {
		t.Fatalf("ParseMessage error = %v", err)
	}
	if msg.Kind != KindRequest || msg.Method != "sampling/createMessage" {
		t.Errorf("got kind %v method %q, want request sampling/createMessage", msg.Kind, msg.Method)
	}
}

func TestParseIgnoresUnknownFields(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":{},"extra":{"nested":[1,2]}}`))
	if err != nil {
		t.Fatalf("ParseMessage error = %v", err)
	}
	if msg.Kind != KindResponse {
		t.Errorf("Kind = %v, want response", msg.Kind)
	}
}

func TestMarshalRequestRoundTrip(t *testing.T) {
	out, err := MarshalRequest(7, "tools/list", map[string]any{"cursor": "c"})
	if err != nil {
		t.Fatalf("MarshalRequest error = %v", err)
	}
	msg, err := ParseMessage(out)
	if err != nil {
		t.Fatalf("ParseMessage(marshalled) error = %v", err)
	}
	if msg.Kind != KindRequest || msg.ID != "7" || msg.Method != "tools/list" {
		t.Errorf("round trip got kind %v id %q method %q", msg.Kind, msg.ID, msg.Method)
	}
	var params map[string]any
	if err := json.Unmarshal(msg.Params, &params); err != nil || params["cursor"] != "c" {
		t.Errorf("params = %s, want cursor c", msg.Params)
	}
}

func TestMarshalNotificationRoundTrip(t *testing.T) {
	out, err := MarshalNotification("notifications/initialized", map[string]any{})
	if err != nil {
		t.Fatalf("MarshalNotification error = %v", err)
	}
	msg, err := ParseMessage(out)
	if err != nil {
		t.Fatalf("ParseMessage(marshalled) error = %v", err)
	}
	if msg.Kind != KindNotification || msg.Method != "notifications/initialized" {
		t.Errorf("round trip got kind %v method %q", msg.Kind, msg.Method)
	}
}

func TestMarshalRejectsEmptyMethod(t *testing.T) {
	if _, err := MarshalRequest(1, " ", nil); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("MarshalRequest empty method err = %v, want ErrInvalidMessage", err)
	}
	if _, err := MarshalNotification("", nil); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("MarshalNotification empty method err = %v, want ErrInvalidMessage", err)
	}
}

func TestNormalizeIDEquivalence(t *testing.T) {
	a, err := NormalizeID([]byte(`1`))
	if err != nil {
		t.Fatalf("NormalizeID(1) error = %v", err)
	}
	b, err := NormalizeID([]byte(`1.0`))
	if err != nil {
		t.Fatalf("NormalizeID(1.0) error = %v", err)
	}
	if a != b {
		t.Errorf("NormalizeID(1) = %q, NormalizeID(1.0) = %q, want equal keys", a, b)
	}
	if got, _ := NormalizeID([]byte(`"x"`)); got != "x" {
		t.Errorf("NormalizeID(\"x\") = %q, want x", got)
	}
}

func TestHostileStringsDoNotPanic(t *testing.T) {
	hostile := []string{
		strings.Repeat("{", 1024),
		strings.Repeat("[", 512) + strings.Repeat("]", 512),
		"{\"jsonrpc\":\"2.0\",\"id\":\"" + strings.Repeat("é", 5000) + "\",\"method\":\"m\"}",
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"" + strings.Repeat("\\u0000", 100) + "\"}",
		"\xff\xfe\x00malformed",
		strings.Repeat(`{"a":`, 2000),
	}
	for i, raw := range hostile {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("case %d panicked: %v", i, p)
				}
			}()
			_, _ = ParseMessage([]byte(raw))
			_, _ = NormalizeID([]byte(raw))
		}()
	}
}
