package command

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeContext is an in-memory Context used across tests in this package.
// It records everything commands do to it, without pulling in the TUI or
// the runtime.
type fakeContext struct {
	lines    []string
	cleared  bool
	quit     bool
	model    string
	provider string

	setModelErr    error
	setProviderErr error
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

// echoCommand records the args it was called with and can be told to fail.
type echoCommand struct {
	calledWith []string
	failWith   error
}

func (c *echoCommand) Name() string        { return "echo" }
func (c *echoCommand) Aliases() []string   { return []string{"e"} }
func (c *echoCommand) Description() string { return "echoes its arguments" }
func (c *echoCommand) Usage() string       { return "/echo [args...]" }
func (c *echoCommand) Execute(ctx Context, args []string) error {
	c.calledWith = args
	if c.failWith != nil {
		return c.failWith
	}
	ctx.Println("echo: %s", strings.Join(args, " "))
	return nil
}

func TestDispatch_NonCommandPassesThrough(t *testing.T) {
	reg := NewRegistry()
	ctx := &fakeContext{}

	isCommand, err := Dispatch(ctx, reg, "hello there")
	if isCommand {
		t.Fatalf("Dispatch reported a plain chat message as a command")
	}
	if err != nil {
		t.Fatalf("Dispatch returned an error for non-command input: %v", err)
	}
}

func TestDispatch_RunsCommandWithArgs(t *testing.T) {
	reg := NewRegistry()
	cmd := &echoCommand{}
	reg.Register(cmd)
	ctx := &fakeContext{}

	isCommand, err := Dispatch(ctx, reg, "/echo one two")
	if !isCommand || err != nil {
		t.Fatalf("Dispatch(/echo one two) = %v, %v; want true, nil", isCommand, err)
	}
	if want := []string{"one", "two"}; strings.Join(cmd.calledWith, ",") != strings.Join(want, ",") {
		t.Fatalf("command received args %v, want %v", cmd.calledWith, want)
	}
	if len(ctx.lines) != 1 || ctx.lines[0] != "echo: one two" {
		t.Fatalf("ctx.lines = %v, want [\"echo: one two\"]", ctx.lines)
	}
}

func TestDispatch_ResolvesAlias(t *testing.T) {
	reg := NewRegistry()
	cmd := &echoCommand{}
	reg.Register(cmd)
	ctx := &fakeContext{}

	isCommand, err := Dispatch(ctx, reg, "/e hi")
	if !isCommand || err != nil {
		t.Fatalf("Dispatch(/e hi) = %v, %v; want true, nil", isCommand, err)
	}
	if len(cmd.calledWith) != 1 || cmd.calledWith[0] != "hi" {
		t.Fatalf("alias dispatch did not reach the aliased command: %v", cmd.calledWith)
	}
}

func TestDispatch_UnknownCommandSuggestsClosestMatch(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&echoCommand{})
	ctx := &fakeContext{}

	isCommand, err := Dispatch(ctx, reg, "/ehco")
	if !isCommand {
		t.Fatalf("Dispatch reported an unknown slash command as not a command")
	}
	if err == nil {
		t.Fatalf("Dispatch did not return an error for an unknown command")
	}
	if !strings.Contains(err.Error(), "/echo") {
		t.Fatalf("error %q does not suggest the close match /echo", err.Error())
	}
}

func TestDispatch_CommandExecutionErrorIsWrapped(t *testing.T) {
	reg := NewRegistry()
	failure := errors.New("boom")
	reg.Register(&echoCommand{failWith: failure})
	ctx := &fakeContext{}

	isCommand, err := Dispatch(ctx, reg, "/echo")
	if !isCommand {
		t.Fatalf("Dispatch reported a recognized command as not a command")
	}
	if !errors.Is(err, failure) {
		t.Fatalf("Dispatch(err) = %v, want it to wrap %v", err, failure)
	}
}
