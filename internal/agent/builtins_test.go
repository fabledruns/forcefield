package agent

import (
	"strings"
	"testing"
)

func TestBuiltins_HaveDistinctTools(t *testing.T) {
	r := DefaultRegistry()
	coding, _ := r.Get("coding")
	legal, _ := r.Get("legal")
	cyber, _ := r.Get("cyber")
	docs, _ := r.Get("docs")
	research, _ := r.Get("research")
	devops, _ := r.Get("devops")
	general, _ := r.Get("general")

	contains := func(tools []string, name string) bool {
		for _, t := range tools {
			if t == name {
				return true
			}
		}
		return false
	}

	// coding == general (full)
	if len(coding.Tools) != len(general.Tools) {
		t.Fatalf("coding and general should have same tool count")
	}
	if !contains(coding.Tools, "edit_file") {
		t.Fatalf("coding should have edit_file")
	}
	// cyber has shell but neither write_file nor edit_file
	if !contains(cyber.Tools, "shell") {
		t.Fatalf("cyber should have shell")
	}
	if contains(cyber.Tools, "write_file") || contains(cyber.Tools, "edit_file") {
		t.Fatalf("cyber should not have write_file or edit_file")
	}
	// legal has neither shell nor write_file nor edit_file
	if contains(legal.Tools, "shell") || contains(legal.Tools, "write_file") || contains(legal.Tools, "edit_file") {
		t.Fatalf("legal should have neither shell nor write_file nor edit_file")
	}
	// docs has write_file and edit_file but not shell
	if !contains(docs.Tools, "write_file") || !contains(docs.Tools, "edit_file") {
		t.Fatalf("docs should have write_file and edit_file")
	}
	if contains(docs.Tools, "shell") {
		t.Fatalf("docs should not have shell")
	}
	// research has none of them
	if contains(research.Tools, "shell") || contains(research.Tools, "write_file") || contains(research.Tools, "edit_file") {
		t.Fatalf("research should have neither")
	}
	// devops has shell, write_file, and edit_file
	if !contains(devops.Tools, "shell") || !contains(devops.Tools, "write_file") || !contains(devops.Tools, "edit_file") {
		t.Fatalf("devops should have shell, write_file, and edit_file")
	}
}

func TestBuiltins_PromptsAreDistinct(t *testing.T) {
	r := DefaultRegistry()
	coding, _ := r.Get("coding")
	cyber, _ := r.Get("cyber")
	legal, _ := r.Get("legal")
	if coding.SystemPrompt == cyber.SystemPrompt {
		t.Fatalf("coding and cyber prompts should differ")
	}
	if coding.SystemPrompt == legal.SystemPrompt {
		t.Fatalf("coding and legal prompts should differ")
	}
	if !strings.Contains(strings.ToLower(cyber.SystemPrompt), "defensive") {
		t.Fatalf("cyber prompt should mention defensive")
	}
	if !strings.Contains(strings.ToLower(legal.SystemPrompt), "not a lawyer") {
		t.Fatalf("legal prompt should disclaim lawyer")
	}
}

func TestBuiltins_ToolValidation(t *testing.T) {
	r := DefaultRegistry()
	for _, d := range r.List() {
		if err := d.Validate(); err != nil {
			t.Fatalf("builtin %q invalid: %v", d.Name, err)
		}
	}
}
