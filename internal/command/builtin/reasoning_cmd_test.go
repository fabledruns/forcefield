package builtin

import (
	"strings"
	"testing"

	"forcefield/internal/providers"
)

func TestEffort_NoArgsUnsupported(t *testing.T) {
	ctx := &fakeContext{provider: "ollama", model: "ornith:9b"} // ollama does not support effort
	if err := NewEffort().Execute(ctx, nil); err != nil {
		t.Fatalf("Effort unsupported bare should not error, got %v", err)
	}
	if len(ctx.lines) == 0 || !strings.Contains(ctx.lines[0], "does not support") {
		t.Fatalf("effort unsupported output = %v, want not support message", ctx.lines)
	}
}

func TestEffort_NoArgsShowsCurrentAndAvailable(t *testing.T) {
	ctx := &fakeContext{provider: "nvidia", model: "z-ai/glm-5.2"}
	// Set effort to medium
	if err := ctx.SetEffort("medium"); err != nil {
		t.Fatalf("SetEffort medium = %v", err)
	}
	ctx.lines = nil
	if err := NewEffort().Execute(ctx, nil); err != nil {
		t.Fatalf("Effort bare = %v", err)
	}
	if len(ctx.lines) == 0 || !strings.Contains(ctx.lines[0], "medium") || !strings.Contains(ctx.lines[0], "low") {
		t.Fatalf("effort bare output = %v, want current medium and available", ctx.lines)
	}
}

func TestEffort_WithArgValidSets(t *testing.T) {
	ctx := &fakeContext{provider: "nvidia", model: "z-ai/glm-5.2"}
	if err := NewEffort().Execute(ctx, []string{"high"}); err != nil {
		t.Fatalf("Effort high = %v", err)
	}
	if ctx.effort != "high" {
		t.Errorf("effort = %q, want high", ctx.effort)
	}
	if len(ctx.lines) == 0 || !strings.Contains(ctx.lines[0], "✓ Effort") {
		t.Errorf("effort output = %v, want success", ctx.lines)
	}
}

func TestEffort_WithArgInvalidDoesNotMutate(t *testing.T) {
	ctx := &fakeContext{provider: "nvidia", model: "z-ai/glm-5.2"}
	if err := ctx.SetEffort("low"); err != nil {
		t.Fatalf("setup low = %v", err)
	}
	err := NewEffort().Execute(ctx, []string{"ultra"})
	if err == nil {
		t.Fatal("Effort ultra should error")
	}
	if !strings.Contains(err.Error(), "Supported levels") {
		t.Errorf("error = %q, want Supported levels", err.Error())
	}
	if ctx.effort != "low" {
		t.Errorf("effort after invalid = %q, want still low", ctx.effort)
	}
}

func TestEffort_UnsupportedWithArgReturnsError(t *testing.T) {
	ctx := &fakeContext{provider: "ollama", model: "ornith:9b"}
	if err := NewEffort().Execute(ctx, []string{"high"}); err == nil {
		t.Fatal("Effort high for unsupported should error")
	}
}

func TestEffort_CaseInsensitiveAndCanonical(t *testing.T) {
	ctx := &fakeContext{provider: "nvidia", model: "z-ai/glm-5.2"}
	if err := NewEffort().Execute(ctx, []string{"HIGH"}); err != nil {
		t.Fatalf("Effort HIGH = %v", err)
	}
	if ctx.effort != "high" {
		t.Errorf("effort = %q, want canonical high", ctx.effort)
	}
}

func TestEffort_TooManyArgs(t *testing.T) {
	ctx := &fakeContext{provider: "nvidia", model: "z-ai/glm-5.2"}
	if err := NewEffort().Execute(ctx, []string{"low", "high"}); err == nil {
		t.Fatal("Effort with 2 args should error")
	}
}

func TestThinking_Bool_NoArgsToggles(t *testing.T) {
	ctx := &fakeContext{provider: "ollama", model: "ornith:9b"}
	// Initially off (nil)
	if err := NewThinking().Execute(ctx, nil); err != nil {
		t.Fatalf("Thinking bare toggle on = %v", err)
	}
	if ctx.thinking == nil || ctx.thinking.Enabled == nil || !*ctx.thinking.Enabled {
		t.Fatalf("thinking after first toggle = %v, want on", ctx.thinking)
	}
	ctx.lines = nil
	if err := NewThinking().Execute(ctx, nil); err != nil {
		t.Fatalf("Thinking bare toggle off = %v", err)
	}
	if ctx.thinking.Enabled == nil || *ctx.thinking.Enabled {
		t.Fatalf("thinking after second toggle = %v, want off", ctx.thinking)
	}
}

func TestThinking_Bool_WithArgOnOff(t *testing.T) {
	ctx := &fakeContext{provider: "ollama", model: "ornith:9b"}
	if err := NewThinking().Execute(ctx, []string{"on"}); err != nil {
		t.Fatalf("Thinking on = %v", err)
	}
	if !*ctx.thinking.Enabled {
		t.Errorf("thinking on failed")
	}
	if err := NewThinking().Execute(ctx, []string{"off"}); err != nil {
		t.Fatalf("Thinking off = %v", err)
	}
	if *ctx.thinking.Enabled {
		t.Errorf("thinking off failed")
	}
	// Case insensitive
	if err := NewThinking().Execute(ctx, []string{"ON"}); err != nil {
		t.Fatalf("Thinking ON = %v", err)
	}
}

func TestThinking_Bool_InvalidValue(t *testing.T) {
	ctx := &fakeContext{provider: "ollama", model: "ornith:9b"}
	if err := NewThinking().Execute(ctx, []string{"maybe"}); err == nil {
		t.Fatal("Thinking maybe should error")
	}
}

func TestThinking_Budget_NoArgsShowsRange(t *testing.T) {
	ctx := &fakeContext{provider: "anthropic", model: "claude-sonnet-4-5"}
	if err := NewThinking().Execute(ctx, nil); err != nil {
		t.Fatalf("Thinking bare budget = %v", err)
	}
	if len(ctx.lines) == 0 || !strings.Contains(ctx.lines[0], "budget") {
		t.Fatalf("budget bare output = %v, want budget range", ctx.lines)
	}
	b := 4096
	ctx.thinking = &providers.ThinkingConfig{Budget: &b}
	ctx.lines = nil
	if err := NewThinking().Execute(ctx, nil); err != nil {
		t.Fatalf("Thinking bare with set = %v", err)
	}
	if !strings.Contains(ctx.lines[0], "4096") {
		t.Fatalf("budget set output = %v, want 4096", ctx.lines)
	}
}

func TestThinking_Budget_WithArgValid(t *testing.T) {
	ctx := &fakeContext{provider: "anthropic", model: "claude-sonnet-4-5"}
	if err := NewThinking().Execute(ctx, []string{"4096"}); err != nil {
		t.Fatalf("Thinking 4096 = %v", err)
	}
	if ctx.thinking == nil || ctx.thinking.Budget == nil || *ctx.thinking.Budget != 4096 {
		t.Fatalf("thinking budget = %v, want 4096", ctx.thinking)
	}
}

func TestThinking_Budget_InvalidDoesNotMutate(t *testing.T) {
	ctx := &fakeContext{provider: "anthropic", model: "claude-sonnet-4-5"}
	b := 4096
	ctx.thinking = &providers.ThinkingConfig{Budget: &b}
	if err := NewThinking().Execute(ctx, []string{"10"}); err == nil {
		t.Fatal("Thinking 10 should error (below min)")
	}
	if *ctx.thinking.Budget != 4096 {
		t.Errorf("budget after invalid = %d, want 4096", *ctx.thinking.Budget)
	}
}

func TestThinking_Budget_OnOff(t *testing.T) {
	ctx := &fakeContext{provider: "anthropic", model: "claude-sonnet-4-5"}
	if err := NewThinking().Execute(ctx, []string{"on"}); err != nil {
		t.Fatalf("Thinking on = %v", err)
	}
	if ctx.thinking.Enabled == nil || !*ctx.thinking.Enabled {
		t.Errorf("thinking on enabled = %v", ctx.thinking)
	}
	if err := NewThinking().Execute(ctx, []string{"off"}); err != nil {
		t.Fatalf("Thinking off = %v", err)
	}
	if ctx.thinking.Enabled == nil || *ctx.thinking.Enabled {
		t.Errorf("thinking off enabled = %v", ctx.thinking)
	}
}

func TestThinking_Unsupported(t *testing.T) {
	ctx := &fakeContext{provider: "lmstudio", model: "local-model"}
	if err := NewThinking().Execute(ctx, nil); err != nil {
		t.Fatalf("unsupported bare should not error, got %v", err)
	}
	if len(ctx.lines) == 0 || !strings.Contains(ctx.lines[0], "does not support") {
		t.Fatalf("unsupported output = %v", ctx.lines)
	}
	if err := NewThinking().Execute(ctx, []string{"on"}); err == nil {
		t.Fatal("unsupported with arg should error")
	}
}

func TestThinking_TooManyArgs(t *testing.T) {
	ctx := &fakeContext{provider: "ollama", model: "ornith:9b"}
	if err := NewThinking().Execute(ctx, []string{"on", "off"}); err == nil {
		t.Fatal("too many args should error")
	}
}

func TestThinking_EffortDistinct(t *testing.T) {
	// Model nvidia supports both effort and thinking, they should be independent.
	ctx := &fakeContext{provider: "nvidia", model: "z-ai/glm-5.2"}
	if err := NewEffort().Execute(ctx, []string{"high"}); err != nil {
		t.Fatalf("effort high = %v", err)
	}
	if err := NewThinking().Execute(ctx, []string{"off"}); err != nil {
		t.Fatalf("thinking off = %v", err)
	}
	if ctx.effort != "high" {
		t.Errorf("effort = %q, want high", ctx.effort)
	}
	if ctx.thinking == nil || *ctx.thinking.Enabled {
		t.Errorf("thinking should be off after effort high")
	}
	// Ensure thinking toggle doesn't affect effort
	on := true
	ctx.thinking = &providers.ThinkingConfig{Enabled: &on}
	// Simulate toggling thinking should not clear effort
	if err := NewThinking().Execute(ctx, nil); err != nil { // toggle to off
		t.Fatalf("toggle = %v", err)
	}
	if ctx.effort != "high" {
		t.Errorf("effort after thinking toggle = %q, want still high", ctx.effort)
	}
}

func TestEffort_DeepSeek_NoneIsEffortNotThinking(t *testing.T) {
	ctx := &fakeContext{provider: "nvidia", model: "deepseek-ai/deepseek-v4-flash-0731"}
	if err := NewEffort().Execute(ctx, []string{"none"}); err != nil {
		t.Fatalf("effort none for DeepSeek = %v", err)
	}
	if ctx.effort != "none" {
		t.Errorf("effort = %q, want none", ctx.effort)
	}
	// /thinking should be unsupported for DeepSeek (none is effort, not thinking off)
	ctx.lines = nil
	if err := NewThinking().Execute(ctx, nil); err != nil {
		t.Fatalf("DeepSeek thinking bare should not error, got %v", err)
	}
	if len(ctx.lines) == 0 || !strings.Contains(ctx.lines[0], "does not support") {
		t.Fatalf("DeepSeek thinking output = %v, want unsupported", ctx.lines)
	}
	if err := NewThinking().Execute(ctx, []string{"off"}); err == nil {
		t.Fatal("DeepSeek thinking off should error (unsupported)")
	}
	// Valid effort levels for DeepSeek: none, high, max
	for _, lvl := range []string{"high", "max"} {
		if err := NewEffort().Execute(ctx, []string{lvl}); err != nil {
			t.Fatalf("DeepSeek effort %q = %v", lvl, err)
		}
	}
	// Invalid: low, medium, xhigh, minimal should fail
	for _, invalid := range []string{"low", "medium", "xhigh", "minimal"} {
		if err := NewEffort().Execute(ctx, []string{invalid}); err == nil {
			t.Errorf("DeepSeek effort %q should be invalid", invalid)
		}
	}
}

func TestEffort_MuseGlimmer_APILevelsOnly(t *testing.T) {
	ctx := &fakeContext{provider: "nvidia", model: "meta/muse-glimmer-30b"}
	// Valid API levels
	for _, lvl := range []string{"none", "minimal", "low", "medium", "high", "max"} {
		if err := NewEffort().Execute(ctx, []string{lvl}); err != nil {
			t.Fatalf("Muse effort %q = %v", lvl, err)
		}
		if ctx.effort != lvl {
			t.Errorf("Muse effort = %q, want %q", ctx.effort, lvl)
		}
	}
	// xhigh should be invalid (model-card term not API)
	if err := NewEffort().Execute(ctx, []string{"xhigh"}); err == nil {
		t.Fatal("Muse effort xhigh should be invalid (API uses max, not xhigh)")
	}
	// Thinking should be unsupported for Muse per verified API (effort controls reasoning)
	ctx.lines = nil
	if err := NewThinking().Execute(ctx, nil); err != nil {
		t.Fatalf("Muse thinking bare = %v", err)
	}
	if !strings.Contains(ctx.lines[0], "does not support") {
		t.Fatalf("Muse thinking should be unsupported, got %v", ctx.lines)
	}
}

func TestEffort_MuseGlimmer_PrefixedModelID(t *testing.T) {
	// Also support nvidia/meta/muse-glimmer-30b via normalization
	ctx := &fakeContext{provider: "nvidia", model: "nvidia/meta/muse-glimmer-30b"}
	if err := NewEffort().Execute(ctx, []string{"high"}); err != nil {
		t.Fatalf("prefixed Muse effort high = %v", err)
	}
	if ctx.effort != "high" {
		t.Errorf("prefixed effort = %q, want high", ctx.effort)
	}
}
