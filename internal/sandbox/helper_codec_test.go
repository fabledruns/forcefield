package sandbox

import (
	"bytes"
	"strings"
	"testing"

	"forcefield/internal/sandbox/landlock"
)

func encodeHelperRequest(t *testing.T, req helperRequest) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := writeHelperRequest(&b, req); err != nil {
		t.Fatalf("writeHelperRequest: %v", err)
	}
	return b.Bytes()
}

func validHelperRequest() helperRequest {
	return helperRequest{
		Version:    helperVersion,
		Command:    "true",
		Dir:        "/tmp/ws",
		Env:        []string{"PATH=/usr/bin:/bin"},
		Bash:       "/bin/bash",
		Rules:      []landlock.Rule{{Path: "/tmp/ws"}},
		NoNewPrivs: true,
	}
}

func TestHelperRequestRoundTrip(t *testing.T) {
	want := validHelperRequest()
	got, err := readHelperRequest(bytes.NewReader(encodeHelperRequest(t, want)))
	if err != nil {
		t.Fatalf("readHelperRequest: %v", err)
	}
	if got.Version != want.Version || got.Command != want.Command || got.Dir != want.Dir ||
		got.Bash != want.Bash || got.NoNewPrivs != want.NoNewPrivs ||
		len(got.Env) != 1 || len(got.Rules) != 1 || got.Rules[0] != want.Rules[0] {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestHelperRequestRejects(t *testing.T) {
	mutate := func(mut func(*helperRequest)) []byte {
		req := validHelperRequest()
		mut(&req)
		return encodeHelperRequest(t, req)
	}
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"empty", nil, "truncated"},
		{"bad magic", []byte("XXXXXXXX\x00\x00\x00\x01Z"), "magic"},
		{"bad version", mutate(func(r *helperRequest) { r.Version = 99 }), "version"},
		{"empty command", mutate(func(r *helperRequest) { r.Command = "" }), "command"},
		{"empty bash", mutate(func(r *helperRequest) { r.Bash = "" }), "interpreter"},
		{"empty dir", mutate(func(r *helperRequest) { r.Dir = "" }), "directory"},
		{"no rules", mutate(func(r *helperRequest) { r.Rules = nil }), "no allow rules"},
	}
	for _, tc := range cases {
		if _, err := readHelperRequest(bytes.NewReader(tc.data)); err == nil {
			t.Errorf("%s: accepted, want rejection", tc.name)
		} else if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}
