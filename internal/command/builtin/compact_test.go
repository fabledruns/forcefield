package builtin

import (
	"strings"
	"testing"
)

// TestCompact_ReportsState is the smallest /compact behavior: it reports
// the automatic compaction state without mutating anything.
func TestCompact_ReportsState(t *testing.T) {
	ctx := &fakeContext{info: contextInfo()}
	if err := NewCompact().Execute(ctx, nil); err != nil {
		t.Fatalf("Compact.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "Compacted:") {
		t.Fatalf("/compact output missing Compacted: line:\n%s", out)
	}
}

func TestCompact_PrintsWindowAndDigest(t *testing.T) {
	info := contextInfo()
	info.Compacted = 7
	info.Summarize = true
	ctx := &fakeContext{info: info}
	if err := NewCompact().Execute(ctx, nil); err != nil {
		t.Fatalf("Compact.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	for _, want := range []string{"Messages:", "Summary digest:"} {
		if !strings.Contains(out, want) {
			t.Errorf("/compact output missing %q:\n%s", want, out)
		}
	}
}

func TestCompact_RejectsArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewCompact().Execute(ctx, []string{"now"}); err == nil {
		t.Fatal("Compact.Execute accepted unexpected arguments")
	}
}
