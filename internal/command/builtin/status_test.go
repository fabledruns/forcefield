package builtin

import (
	"strings"
	"testing"

	"forcefield/internal/command"
)

func TestStatusReportsContextState(t *testing.T) {
	ctx := &fakeContext{
		model:    "ornith:9b",
		provider: "ollama",
		stats:    command.SessionStats{ID: "test-session", Messages: 2, Chars: 100},
	}

	if err := NewStatus().Execute(ctx, nil); err != nil {
		t.Fatalf("Status.Execute error = %v", err)
	}

	out := strings.Join(ctx.lines, "\n")
	for _, want := range []string{
		"Provider:  ollama",
		"Model:     ornith:9b",
		"Session:   test-session",
		"Messages:  2 (~100 B of context)",
		"Tools:     1 available",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestStatusHandlesZeroValues(t *testing.T) {
	// A context reporting zeroed stats (no session yet) must still render
	// without panicking or printing garbage.
	fake := &fakeContext{}
	if err := NewStatus().Execute(fake, nil); err != nil {
		t.Fatalf("Status.Execute error = %v", err)
	}
	out := strings.Join(fake.lines, "\n")
	if !strings.Contains(out, "Messages:  0") {
		t.Errorf("status output missing zero message count:\n%s", out)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int]string{
		0:           "0 B",
		512:         "512 B",
		2048:        "2.0 KB",
		5 * 1024:    "5.0 KB",
		1536 * 1024: "1.5 MB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestToolsListsRegisteredTools(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewTools().Execute(ctx, nil); err != nil {
		t.Fatalf("Tools.Execute error = %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "read_file") || !strings.Contains(out, "Available tools") {
		t.Errorf("tools output = %q", out)
	}
}

// compile-time interface check for the new Context methods
var _ command.Context = (*fakeContext)(nil)
