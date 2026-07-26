package tools

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// stubTool is a minimal Tool used only by these tests.
type stubTool struct {
	name string
}

func (s *stubTool) Name() string                { return s.name }
func (s *stubTool) Description() string         { return "stub: " + s.name }
func (s *stubTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (s *stubTool) Execute(context.Context, map[string]any) (Result, error) {
	return Result{Content: "ok"}, nil
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	reg := NewRegistry()
	pwd := &stubTool{name: "pwd"}

	if err := reg.Register(pwd); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}

	got, ok := reg.Lookup("pwd")
	if !ok || got != Tool(pwd) {
		t.Fatalf("Lookup(%q) = %v, %v; want %v, true", "pwd", got, ok, pwd)
	}

	if _, ok := reg.Lookup("nope"); ok {
		t.Fatalf("Lookup(%q) unexpectedly found a tool", "nope")
	}
}

func TestRegistry_RegisterRejectsDuplicateName(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(&stubTool{name: "read_file"}); err != nil {
		t.Fatalf("first Register() unexpected error: %v", err)
	}

	err := reg.Register(&stubTool{name: "read_file"})
	if err == nil {
		t.Fatal("second Register() with duplicate name = nil error, want an error")
	}
	if !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("Register() error = %v, want it to wrap ErrAlreadyRegistered", err)
	}
}

func TestRegistry_RegisterRejectsEmptyName(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(&stubTool{name: ""}); err == nil {
		t.Fatal("Register() with empty name = nil error, want an error")
	}
}

func TestRegistry_AllReturnsEachToolOnceInRegistrationOrder(t *testing.T) {
	reg := NewRegistry()
	a := &stubTool{name: "a"}
	b := &stubTool{name: "b"}
	c := &stubTool{name: "c"}

	for _, tool := range []Tool{a, b, c} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register(%q) unexpected error: %v", tool.Name(), err)
		}
	}

	got := reg.All()
	want := []Tool{a, b, c}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("All() = %v, want %v", got, want)
	}
}

func TestRegistry_Definitions(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(&stubTool{name: "pwd"}); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}

	defs := reg.Definitions()
	if len(defs) != 1 {
		t.Fatalf("Definitions() returned %d entries, want 1", len(defs))
	}
	if defs[0].Name != "pwd" || defs[0].Description != "stub: pwd" {
		t.Fatalf("Definitions()[0] = %+v, want Name=pwd Description=%q", defs[0], "stub: pwd")
	}
}
