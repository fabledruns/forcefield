package command

import (
	"reflect"
	"testing"
)

// stubCommand is a minimal Command used only by these tests.
type stubCommand struct {
	name    string
	aliases []string
}

func (c *stubCommand) Name() string                    { return c.name }
func (c *stubCommand) Aliases() []string               { return c.aliases }
func (c *stubCommand) Description() string             { return "stub: " + c.name }
func (c *stubCommand) Usage() string                   { return "/" + c.name }
func (c *stubCommand) Execute(Context, []string) error { return nil }

func TestRegistry_LookupByNameAndAlias(t *testing.T) {
	reg := NewRegistry()
	exit := &stubCommand{name: "exit", aliases: []string{"quit"}}
	reg.Register(exit)

	got, ok := reg.Lookup("exit")
	if !ok || got != Command(exit) {
		t.Fatalf("Lookup(%q) = %v, %v; want %v, true", "exit", got, ok, exit)
	}

	got, ok = reg.Lookup("quit")
	if !ok || got != Command(exit) {
		t.Fatalf("Lookup(%q) = %v, %v; want the same *stubCommand instance", "quit", got, ok)
	}

	if _, ok := reg.Lookup("nope"); ok {
		t.Fatalf("Lookup(%q) unexpectedly found a command", "nope")
	}
}

func TestRegistry_AliasSharesInstance(t *testing.T) {
	reg := NewRegistry()
	exit := &stubCommand{name: "exit", aliases: []string{"quit"}}
	reg.Register(exit)

	byName, _ := reg.Lookup("exit")
	byAlias, _ := reg.Lookup("quit")

	if byName != byAlias {
		t.Fatalf("alias resolved to a different Command instance than the canonical name")
	}
}

func TestRegistry_RegisterPanicsOnDuplicateName(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubCommand{name: "help"})

	defer func() {
		if recover() == nil {
			t.Fatal("Register did not panic on duplicate name")
		}
	}()
	reg.Register(&stubCommand{name: "help"})
}

func TestRegistry_RegisterPanicsOnAliasCollision(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubCommand{name: "exit", aliases: []string{"quit"}})

	defer func() {
		if recover() == nil {
			t.Fatal("Register did not panic on alias colliding with an existing name")
		}
	}()
	reg.Register(&stubCommand{name: "quit"})
}

func TestRegistry_AllReturnsEachCommandOnceInRegistrationOrder(t *testing.T) {
	reg := NewRegistry()
	help := &stubCommand{name: "help", aliases: []string{"?"}}
	clear := &stubCommand{name: "clear"}
	exit := &stubCommand{name: "exit", aliases: []string{"quit"}}

	reg.Register(help)
	reg.Register(clear)
	reg.Register(exit)

	got := reg.All()
	want := []Command{help, clear, exit}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("All() = %v, want %v", got, want)
	}
}

func TestRegistry_Suggest(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubCommand{name: "help"})
	reg.Register(&stubCommand{name: "exit", aliases: []string{"quit"}})
	reg.Register(&stubCommand{name: "clear"})
	reg.Register(&stubCommand{name: "model"})

	got := reg.Suggest("hlep", 3)
	if len(got) == 0 || got[0] != "help" {
		t.Fatalf("Suggest(%q) = %v, want first suggestion %q", "hlep", got, "help")
	}

	got = reg.Suggest("exi", 3)
	if len(got) == 0 || got[0] != "exit" {
		t.Fatalf("Suggest(%q) = %v, want first suggestion %q", "exi", got, "exit")
	}

	// Something with no close match should yield no noisy suggestions.
	got = reg.Suggest("zzzzzzzzzzzzzzzz", 3)
	if len(got) != 0 {
		t.Fatalf("Suggest(%q) = %v, want no suggestions for a wildly unrelated string", "zzzzzzzzzzzzzzzz", got)
	}
}

func TestRegistry_Match(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubCommand{name: "help"})
	reg.Register(&stubCommand{name: "exit", aliases: []string{"quit"}})
	reg.Register(&stubCommand{name: "clear"})
	reg.Register(&stubCommand{name: "model"})

	names := func(cmds []Command) []string {
		out := make([]string, len(cmds))
		for i, c := range cmds {
			out[i] = c.Name()
		}
		return out
	}

	if got := names(reg.Match("")); !reflect.DeepEqual(got, []string{"clear", "exit", "help", "model"}) {
		t.Errorf(`Match("") = %v, want all commands sorted alphabetically`, got)
	}

	if got := names(reg.Match("h")); !reflect.DeepEqual(got, []string{"help"}) {
		t.Errorf(`Match("h") = %v, want [help]`, got)
	}

	// An alias must never surface as its own match: it's the same
	// command as "exit", not a second one.
	if got := names(reg.Match("qu")); len(got) != 0 {
		t.Errorf(`Match("qu") = %v, want no matches (aliases aren't matched)`, got)
	}

	if got := names(reg.Match("zzz")); len(got) != 0 {
		t.Errorf(`Match("zzz") = %v, want no matches`, got)
	}
}
