package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	d := Definition{Name: "test", Description: "test agent", SystemPrompt: "you are test", Tools: []string{"read_file"}}
	if err := r.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Get("test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "test" || got.Description != "test agent" {
		t.Fatalf("got %#v", got)
	}
}

func TestRegistry_UnknownAgent(t *testing.T) {
	r := DefaultRegistry()
	_, err := r.Get("nonexistent")
	if err == nil {
		t.Fatalf("expected error for unknown agent")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "available:") {
		t.Fatalf("error should list available agents, got %q", err.Error())
	}
}

func TestRegistry_Duplicate(t *testing.T) {
	r := NewRegistry()
	d := Definition{Name: "dup", Description: "x", SystemPrompt: "y", Tools: []string{}}
	if err := r.Register(d); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(d); err == nil {
		t.Fatalf("expected duplicate error")
	} else if !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("want ErrAlreadyRegistered, got %v", err)
	}
}

func TestRegistry_ListAndDefault(t *testing.T) {
	r := DefaultRegistry()
	list := r.List()
	if len(list) != 7 {
		t.Fatalf("want 7 built-ins, got %d", len(list))
	}
	def := r.Default()
	if def.Name != "general" {
		t.Fatalf("default = %q, want general", def.Name)
	}
	// List should be in registration order: coding first, general last
	if list[0].Name != "coding" || list[len(list)-1].Name != "general" {
		t.Fatalf("unexpected order: first %q last %q", list[0].Name, list[len(list)-1].Name)
	}
}

func TestRegistry_CaseInsensitive(t *testing.T) {
	r := DefaultRegistry()
	d, err := r.Get("CODING")
	if err != nil {
		t.Fatalf("Get CODING: %v", err)
	}
	if d.Name != "coding" {
		t.Fatalf("name = %q, want coding", d.Name)
	}
}

func TestRegistry_DefaultRegistryIsIndependent(t *testing.T) {
	a := DefaultRegistry()
	b := DefaultRegistry()
	_ = a.Register(Definition{Name: "extra", Description: "x", SystemPrompt: "y", Tools: []string{}})
	if _, err := b.Get("extra"); err == nil {
		t.Fatalf("modifying one registry affected the other")
	}
}

func TestDefinition_Validate(t *testing.T) {
	cases := []struct {
		name string
		def  Definition
		ok   bool
	}{
		{"valid", Definition{Name: "ok", Description: "d", SystemPrompt: "p", Tools: []string{"read_file"}}, true},
		{"empty name", Definition{Name: "", Description: "d", SystemPrompt: "p", Tools: []string{}}, false},
		{"empty desc", Definition{Name: "x", Description: "", SystemPrompt: "p", Tools: []string{}}, false},
		{"empty prompt", Definition{Name: "x", Description: "d", SystemPrompt: "", Tools: []string{}}, false},
		{"nil tools", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: nil}, false},
		{"duplicate tool", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{"a", "a"}}, false},
		{"empty tool", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{""}}, false},
	}
	for _, c := range cases {
		err := c.def.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}
