package builtin

import (
	"strings"
	"testing"

	"forcefield/internal/command"
)

// TestUsage_PrintsMessages is the smallest /usage behavior: it reports the
// session size through the context without invoking the model.
func TestUsage_PrintsMessages(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewUsage().Execute(ctx, nil); err != nil {
		t.Fatalf("Usage.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "Messages:") {
		t.Fatalf("/usage output missing Messages: line:\n%s", out)
	}
}

func TestUsage_PrintsEstimateAndBudget(t *testing.T) {
	ctx := &fakeContext{info: contextInfo()}
	if err := NewUsage().Execute(ctx, nil); err != nil {
		t.Fatalf("Usage.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	for _, want := range []string{"Est. tokens:", "Budget:", "Fit:"} {
		if !strings.Contains(out, want) {
			t.Errorf("/usage output missing %q:\n%s", want, out)
		}
	}
}

func TestUsage_UnknownWindowIsCountBounded(t *testing.T) {
	info := contextInfo()
	info.Limit = 0
	ctx := &fakeContext{info: info}
	if err := NewUsage().Execute(ctx, nil); err != nil {
		t.Fatalf("Usage.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "unknown") {
		t.Fatalf("/usage output should say the window is unknown:\n%s", out)
	}
	if strings.Contains(out, "Fit:") {
		t.Fatalf("/usage output should not claim a fit without a window:\n%s", out)
	}
}

func TestUsage_RejectsArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewUsage().Execute(ctx, []string{"extra"}); err == nil {
		t.Fatal("Usage.Execute accepted unexpected arguments")
	}
}

func TestUsage_OverBudgetReportsOverage(t *testing.T) {
	info := contextInfo()
	info.EstTokens = info.Limit
	ctx := &fakeContext{info: info}
	if err := NewUsage().Execute(ctx, nil); err != nil {
		t.Fatalf("Usage.Execute returned an error: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "over by") {
		t.Fatalf("/usage output should report the overage:\n%s", out)
	}
}

func contextInfo() command.ContextInfo {
	return command.ContextInfo{
		Messages:    12,
		Chars:       4200,
		EstTokens:   1200,
		Limit:       128000,
		Reserve:     4096,
		MaxMessages: 100,
	}
}
