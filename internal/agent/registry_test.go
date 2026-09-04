package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	d := Definition{Name: "test", Description: "test agent", SystemPrompt: "you are test", Tools: []string{"read_file"}, Skills: []string{"s1"}, Constraints: []string{"stay put"}}
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
	d := Definition{Name: "dup", Description: "x", SystemPrompt: "y", Tools: []string{}, Skills: []string{}}
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
	_ = a.Register(Definition{Name: "extra", Description: "x", SystemPrompt: "y", Tools: []string{}, Skills: []string{}})
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
		{"valid", Definition{Name: "ok", Description: "d", SystemPrompt: "p", Tools: []string{"read_file"}, Skills: []string{"s"}}, true},
		{"valid all-skills", Definition{Name: "ok", Description: "d", SystemPrompt: "p", Tools: []string{}, AllSkills: true}, true},
		{"empty name", Definition{Name: "", Description: "d", SystemPrompt: "p", Tools: []string{}, Skills: []string{}}, false},
		{"empty desc", Definition{Name: "x", Description: "", SystemPrompt: "p", Tools: []string{}, Skills: []string{}}, false},
		{"empty prompt", Definition{Name: "x", Description: "d", SystemPrompt: "", Tools: []string{}, Skills: []string{}}, false},
		{"nil tools", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: nil, Skills: []string{}}, false},
		{"duplicate tool", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{"a", "a"}, Skills: []string{}}, false},
		{"empty tool", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{""}, Skills: []string{}}, false},
		{"nil skills without all", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{}, Skills: nil}, false},
		{"skills with all", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{}, Skills: []string{"s"}, AllSkills: true}, false},
		{"duplicate skill", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{}, Skills: []string{"s", "s"}}, false},
		{"empty skill", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{}, Skills: []string{""}}, false},
		{"empty constraint", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{}, Skills: []string{}, Constraints: []string{""}}, false},
		{"duplicate constraint", Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{}, Skills: []string{}, Constraints: []string{"c", "c"}}, false},
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

func TestApplyOverride_SkillsExplicitSetSemantics(t *testing.T) {
	base := Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{"a"}, Skills: []string{"s1", "s2"}}

	// Omitted (nil) keeps the built-in assignment.
	kept := base.ApplyOverride(AgentOverride{Description: "new"})
	if len(kept.Skills) != 2 || kept.AllSkills {
		t.Fatalf("nil override must keep skills, got %#v", kept.Skills)
	}

	// Explicit list replaces.
	replaced := base.ApplyOverride(AgentOverride{Skills: []string{"s3"}})
	if len(replaced.Skills) != 1 || replaced.Skills[0] != "s3" {
		t.Fatalf("override must replace, got %#v", replaced.Skills)
	}

	// Explicit empty (non-nil) means "no skills" — and stays non-nil
	// through Clone so reloads cannot reinterpret it.
	emptied := base.ApplyOverride(AgentOverride{Skills: []string{}})
	if emptied.Skills == nil || len(emptied.Skills) != 0 {
		t.Fatalf("explicit empty must mean none, got %#v (nil=%v)", emptied.Skills, emptied.Skills == nil)
	}
	if emptied.Clone().Skills == nil {
		t.Fatalf("Clone collapsed explicit empty to nil")
	}

	// Explicit list clears the general agent's all-skills behavior.
	gen := Definition{Name: "general", Description: "d", SystemPrompt: "p", Tools: []string{}, AllSkills: true}
	scoped := gen.ApplyOverride(AgentOverride{Skills: []string{"s1"}})
	if scoped.AllSkills {
		t.Fatalf("explicit skills must clear AllSkills")
	}
}

func TestClone_PreservesExplicitEmpty(t *testing.T) {
	d := Definition{Name: "x", Description: "d", SystemPrompt: "p", Tools: []string{}, Skills: []string{}, Constraints: []string{}}
	c := d.Clone()
	if c.Skills == nil {
		t.Fatalf("Clone must preserve non-nil empty Skills")
	}
	if c.Tools == nil {
		t.Fatalf("Clone must preserve non-nil empty Tools")
	}
}
