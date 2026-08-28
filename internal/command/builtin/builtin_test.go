package builtin

import (
	"fmt"
	"strings"
	"testing"

	"forcefield/internal/command"
	"forcefield/internal/providers"
	"forcefield/internal/session"
)

// fakeContext is a minimal in-memory command.Context, used to test
// builtin commands without a real TUI model or runtime.
type fakeContext struct {
	lines    []string
	cleared  bool
	quit     bool
	model    string
	provider string

	setModelErr    error
	setProviderErr error

	pickedSessions []session.Session

	openedProviderPicker bool
	openedModelPicker    bool

	stats     command.SessionStats
	toolLines []string

	effort   string
	thinking *providers.ThinkingConfig
}

func (f *fakeContext) Println(format string, args ...any) {
	f.lines = append(f.lines, fmt.Sprintf(format, args...))
}
func (f *fakeContext) Clear()           { f.cleared = true }
func (f *fakeContext) Quit()            { f.quit = true }
func (f *fakeContext) Model() string    { return f.model }
func (f *fakeContext) Provider() string { return f.provider }

func (f *fakeContext) SetModel(name string) error {
	if f.setModelErr != nil {
		return f.setModelErr
	}
	f.model = name
	return nil
}

func (f *fakeContext) SetProvider(name string) error {
	if f.setProviderErr != nil {
		return f.setProviderErr
	}
	f.provider = name
	return nil
}

func (f *fakeContext) OpenSessionPicker(sessions []session.Session) {
	f.pickedSessions = sessions
}

func (f *fakeContext) OpenProviderPicker() { f.openedProviderPicker = true }
func (f *fakeContext) OpenModelPicker()    { f.openedModelPicker = true }

func (f *fakeContext) SessionStats() command.SessionStats { return f.stats }

func (f *fakeContext) Tools() []string {
	if len(f.toolLines) == 0 {
		return []string{"read_file: reads files"}
	}
	return f.toolLines
}

func (f *fakeContext) ReasoningCapabilities() providers.ReasoningCapabilities {
	return providers.ModelReasoningCapabilities(f.provider, f.model)
}

func (f *fakeContext) Effort() string { return f.effort }

func (f *fakeContext) SetEffort(level string) error {
	caps := f.ReasoningCapabilities()
	if err := caps.ValidateEffort(level); err != nil {
		return err
	}
	f.effort = caps.CanonicalEffort(level)
	return nil
}

func (f *fakeContext) Thinking() *providers.ThinkingConfig {
	if f.thinking == nil {
		return nil
	}
	deep := providers.ThinkingConfig{Level: f.thinking.Level}
	if f.thinking.Enabled != nil {
		v := *f.thinking.Enabled
		deep.Enabled = &v
	}
	if f.thinking.Budget != nil {
		v := *f.thinking.Budget
		deep.Budget = &v
	}
	return &deep
}

func (f *fakeContext) SetThinking(cfg providers.ThinkingConfig) error {
	caps := f.ReasoningCapabilities()
	if err := caps.ValidateThinking(cfg); err != nil {
		return err
	}
	if caps.Thinking != nil && caps.Thinking.Kind == providers.ThinkingKindEnum && cfg.Level != "" {
		cfg.Level = caps.CanonicalThinkingLevel(cfg.Level)
	}
	deep := providers.ThinkingConfig{Level: cfg.Level}
	if cfg.Enabled != nil {
		v := *cfg.Enabled
		deep.Enabled = &v
	}
	if cfg.Budget != nil {
		v := *cfg.Budget
		deep.Budget = &v
	}
	f.thinking = &deep
	return nil
}

func (f *fakeContext) ToggleThinking() (bool, error) {
	caps := f.ReasoningCapabilities()
	if caps.Thinking == nil || caps.Thinking.Kind != providers.ThinkingKindBool {
		return false, fmt.Errorf("Current model does not support thinking toggle.")
	}
	enabled := true
	if f.thinking != nil && f.thinking.Enabled != nil {
		enabled = !*f.thinking.Enabled
	}
	tc := providers.ThinkingConfig{Enabled: &enabled}
	if err := f.SetThinking(tc); err != nil {
		return false, err
	}
	return enabled, nil
}

func TestExit(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewExit().Execute(ctx, nil); err != nil {
		t.Fatalf("Exit.Execute returned an error: %v", err)
	}
	if !ctx.quit {
		t.Fatal("Exit.Execute did not call ctx.Quit()")
	}
}

func TestClear(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewClear().Execute(ctx, nil); err != nil {
		t.Fatalf("Clear.Execute returned an error: %v", err)
	}
	if !ctx.cleared {
		t.Fatal("Clear.Execute did not call ctx.Clear()")
	}
}

func TestModel_NoArgsOpensPicker(t *testing.T) {
	ctx := &fakeContext{model: "qwen3:8b"}
	if err := NewModel().Execute(ctx, nil); err != nil {
		t.Fatalf("Model.Execute returned an error: %v", err)
	}
	if !ctx.openedModelPicker {
		t.Fatal("Model.Execute with no args did not open the model picker")
	}
	if len(ctx.lines) != 0 {
		t.Fatalf("ctx.lines = %v, want no lines when opening the picker", ctx.lines)
	}
}

func TestModel_WithArgSwitches(t *testing.T) {
	ctx := &fakeContext{model: "qwen3:8b"}
	if err := NewModel().Execute(ctx, []string{"llama3"}); err != nil {
		t.Fatalf("Model.Execute returned an error: %v", err)
	}
	if ctx.model != "llama3" {
		t.Fatalf("ctx.model = %q, want %q", ctx.model, "llama3")
	}
}

func TestModel_PropagatesSetModelError(t *testing.T) {
	ctx := &fakeContext{setModelErr: fmt.Errorf("nope")}
	if err := NewModel().Execute(ctx, []string{"llama3"}); err == nil {
		t.Fatal("Model.Execute did not return the error from ctx.SetModel")
	}
}

func TestModel_RejectsTooManyArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewModel().Execute(ctx, []string{"a", "b"}); err == nil {
		t.Fatal("Model.Execute accepted more than one argument")
	}
}

func TestProvider_NoArgsOpensPicker(t *testing.T) {
	ctx := &fakeContext{provider: "ollama"}
	if err := NewProvider().Execute(ctx, nil); err != nil {
		t.Fatalf("Provider.Execute returned an error: %v", err)
	}
	if !ctx.openedProviderPicker {
		t.Fatal("Provider.Execute with no args did not open the provider picker")
	}
	if len(ctx.lines) != 0 {
		t.Fatalf("ctx.lines = %v, want no lines when opening the picker", ctx.lines)
	}
}

func TestProvider_WithArgSwitches(t *testing.T) {
	ctx := &fakeContext{provider: "ollama"}
	if err := NewProvider().Execute(ctx, []string{"lmstudio"}); err != nil {
		t.Fatalf("Provider.Execute returned an error: %v", err)
	}
	if ctx.provider != "lmstudio" {
		t.Fatalf("ctx.provider = %q, want %q", ctx.provider, "lmstudio")
	}
}

func TestHelp_ListsAllRegisteredCommands(t *testing.T) {
	reg := command.NewRegistry()
	reg.Register(NewExit())
	reg.Register(NewClear())
	reg.Register(NewModel())
	reg.Register(NewProvider())
	help := NewHelp(reg)
	reg.Register(help)

	ctx := &fakeContext{}
	if err := help.Execute(ctx, nil); err != nil {
		t.Fatalf("Help.Execute returned an error: %v", err)
	}
	if len(ctx.lines) != 1 {
		t.Fatalf("Help.Execute produced %d lines, want 1", len(ctx.lines))
	}

	out := ctx.lines[0]
	for _, want := range []string{"/exit", "/clear", "/model", "/provider", "/help", "/quit", "/?"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q:\n%s", want, out)
		}
	}
}
