package builtin

import (
	"strings"
	"testing"

	"forcefield/internal/command"
	"forcefield/internal/skills"
)

func TestSkills_List_Empty(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewSkills().Execute(ctx, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "No skills found") {
		t.Fatalf("expected empty message, got %q", out)
	}
}

func TestSkills_List_WithCatalog(t *testing.T) {
	ctx := &fakeContext{
		skillCatalog: []skills.Skill{
			{ID: "review", Name: "Review", Description: "Do review"},
			{ID: "git-review", Name: "Git Review"},
		},
	}
	if err := NewSkills().Execute(ctx, []string{"list"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "Available skills (2):") {
		t.Fatalf("missing header %q", out)
	}
	if !strings.Contains(out, "review") || !strings.Contains(out, "git-review") {
		t.Fatalf("missing ids %q", out)
	}
}

func TestSkills_Show_Success(t *testing.T) {
	ctx := &fakeContext{
		skillCatalog: []skills.Skill{
			{ID: "review", Name: "Review", Description: "Does review"},
		},
		skillBodies: map[string]string{
			"review": "# Review body\n",
		},
	}
	if err := NewSkills().Execute(ctx, []string{"show", "review"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "Review body") {
		t.Fatalf("expected body, got %q", out)
	}
	if !strings.Contains(out, "Skill `review`") {
		t.Fatalf("expected header, got %q", out)
	}
}

func TestSkills_Show_CaseInsensitiveAndNormalized(t *testing.T) {
	ctx := &fakeContext{
		skillCatalog: []skills.Skill{
			{ID: "go-style-guide", Name: "Go Style"},
		},
		skillBodies: map[string]string{
			"go-style-guide": "body",
		},
	}
	// User types with spaces and caps
	if err := NewSkills().Execute(ctx, []string{"show", "Go", "Style", "Guide"}); err != nil {
		t.Fatalf("Execute with spaces: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "body") {
		t.Fatalf("expected normalized lookup, got %q", out)
	}
}

func TestSkills_Show_NotFound(t *testing.T) {
	ctx := &fakeContext{
		skillCatalog: []skills.Skill{
			{ID: "review", Name: "Review"},
			{ID: "go", Name: "Go"},
		},
		skillBodies: map[string]string{
			"review": "a",
			"go":     "b",
		},
	}
	err := NewSkills().Execute(ctx, []string{"show", "missing"})
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("wrong error %v", err)
	}
	if !strings.Contains(err.Error(), "review") {
		t.Fatalf("should suggest available ids %v", err)
	}
}

func TestSkills_Show_NoArg(t *testing.T) {
	ctx := &fakeContext{}
	err := NewSkills().Execute(ctx, []string{"show"})
	if err == nil {
		t.Fatal("expected error for missing arg")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Fatalf("wrong error %v", err)
	}
}

func TestSkills_UnknownSubcommand(t *testing.T) {
	ctx := &fakeContext{}
	err := NewSkills().Execute(ctx, []string{"enable"})
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
	if !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("wrong error %v", err)
	}
}

func TestSkills_Help(t *testing.T) {
	ctx := &fakeContext{}
	// Help via help subcommand
	if err := NewSkills().Execute(ctx, []string{"help"}); err != nil {
		t.Fatalf("help: %v", err)
	}
	if len(ctx.lines) == 0 {
		t.Fatal("expected help output")
	}
}

func TestSkills_RegisteredInHelp(t *testing.T) {
	reg := command.NewRegistry()
	reg.Register(NewExit())
	reg.Register(NewSkills())
	help := NewHelp(reg)
	reg.Register(help)
	ctx := &fakeContext{}
	if err := help.Execute(ctx, nil); err != nil {
		t.Fatalf("help: %v", err)
	}
	out := strings.Join(ctx.lines, "\n")
	if !strings.Contains(out, "/skills") {
		t.Fatalf("help should list /skills, got %q", out)
	}
}

func TestSkills_AliasSkill(t *testing.T) {
	reg := command.NewRegistry()
	reg.Register(NewSkills())
	if _, ok := reg.Lookup("skill"); !ok {
		t.Fatal("alias /skill not registered")
	}
}
