package builtin

import (
	"strings"
	"testing"
)

// TestContext_PrintsWindow is the smallest /context behavior: it shows what
// the next turn will send without invoking the model.
func TestContext_PrintsWindow(t *testing.T) {
	ctx := &fakeContext{info: contextInfo()}
	if err := NewContext().Execute(ctx, nil); err != nil {
		t.Fatalf("Context.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "Window:") {
		t.Fatalf("/context output missing Window: line:\n%s", out)
	}
}

func TestContext_PrintsSelection(t *testing.T) {
	info := contextInfo()
	info.Kept = 10
	info.Evicted = 2
	ctx := &fakeContext{info: info}
	if err := NewContext().Execute(ctx, nil); err != nil {
		t.Fatalf("Context.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	for _, want := range []string{"kept", "evicted", "Summary:"} {
		if !strings.Contains(out, want) {
			t.Errorf("/context output missing %q:\n%s", want, out)
		}
	}
}

func TestContext_UnknownWindow(t *testing.T) {
	info := contextInfo()
	info.Limit = 0
	ctx := &fakeContext{info: info}
	if err := NewContext().Execute(ctx, nil); err != nil {
		t.Fatalf("Context.Execute returned an error: %v", err)
	}
	if out := strings.Join(ctx.lines, "\n"); !strings.Contains(out, "unknown") {
		t.Fatalf("/context output should say the window is unknown:\n%s", out)
	}
}

func TestContext_RejectsArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewContext().Execute(ctx, []string{"extra"}); err == nil {
		t.Fatal("Context.Execute accepted unexpected arguments")
	}
}
