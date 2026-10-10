package landlock

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func encodePolicy(t *testing.T, p Policy) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := WritePolicy(&b, p); err != nil {
		t.Fatalf("WritePolicy: %v", err)
	}
	return b.Bytes()
}

func TestPolicyRoundTrip(t *testing.T) {
	want := Policy{
		Version:    PolicyVersion,
		NoNewPrivs: true,
		Allowed:    []Rule{{Path: "/tmp/ws", ReadOnly: false}, {Path: "/usr", ReadOnly: true}},
	}
	got, err := ReadPolicy(bytes.NewReader(encodePolicy(t, want)))
	if err != nil {
		t.Fatalf("ReadPolicy: %v", err)
	}
	if got.Version != want.Version || got.NoNewPrivs != want.NoNewPrivs || len(got.Allowed) != 2 ||
		got.Allowed[0] != want.Allowed[0] || got.Allowed[1] != want.Allowed[1] {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestPolicyRejects(t *testing.T) {
	valid := encodePolicy(t, Policy{Version: PolicyVersion, Allowed: []Rule{{Path: "/tmp/x"}}})
	headerLen := 8 + 4

	corrupt := func(mut func([]byte)) []byte {
		b := append([]byte(nil), valid...)
		mut(b)
		return b
	}
	oversized := func() []byte {
		b := append([]byte(nil), valid...)
		binary.BigEndian.PutUint32(b[8:], MaxPolicyBytes+1)
		return b
	}
	unknownField := func() []byte {
		var body bytes.Buffer
		body.WriteString(`{"version":1,"no_new_privs":false,"allowed":[{"path":"/tmp/x","read_only":false}],"future":true}`)
		header := make([]byte, headerLen)
		copy(header, policyMagic)
		binary.BigEndian.PutUint32(header[8:], uint32(body.Len()))
		return append(header, body.Bytes()...)
	}

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"bad magic", corrupt(func(b []byte) { b[0] ^= 0xff }), "magic"},
		{"bad version", encodePolicy(t, Policy{Version: 99, Allowed: []Rule{{Path: "/tmp/x"}}}), "version"},
		{"truncated header", valid[:5], "truncated"},
		{"empty input", nil, "truncated"},
		{"truncated body", valid[:len(valid)-3], "truncated"},
		{"oversized length", oversized(), "exceeds"},
		{"malformed json", corrupt(func(b []byte) { b[len(b)-2] = '{' }), "malformed"},
		{"trailing data", func() []byte {
			raw := encodePolicy(t, Policy{Version: PolicyVersion, Allowed: []Rule{{Path: "/tmp/x"}}})
			body := append(append([]byte(nil), raw[12:]...), "TRAILING"...)
			header := make([]byte, 12)
			copy(header, policyMagic)
			binary.BigEndian.PutUint32(header[8:], uint32(len(body)))
			return append(header, body...)
		}(), "trailing"},
		{"unknown field", unknownField(), "unknown field"},
		{"no allowed paths", encodePolicy(t, Policy{Version: PolicyVersion}), "no allowed"},
		{"empty rule path", encodePolicy(t, Policy{Version: PolicyVersion, Allowed: []Rule{{Path: ""}}}), "empty path"},
	}
	for _, tc := range cases {
		if _, err := ReadPolicy(bytes.NewReader(tc.data)); err == nil {
			t.Errorf("%s: accepted, want rejection", tc.name)
		} else if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}
