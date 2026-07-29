package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/skills"
)

func TestLoadSkillTool_LoadsBody(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: Go\n---\n\n# Go\nPrefer small interfaces.\n"
	if err := os.WriteFile(filepath.Join(dir, "go.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	store, err := skills.New(home)
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}

	tool := newLoadSkillTool(store)
	if tool.Name() != "load_skill" {
		t.Errorf("Name() = %q, want load_skill", tool.Name())
	}

	result, err := tool.Execute(context.Background(), map[string]any{"id": "go"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute IsError: %s", result.Content)
	}
	want := "\n# Go\nPrefer small interfaces.\n"
	if result.Content != want {
		t.Errorf("Content = %q, want %q", result.Content, want)
	}
}

func TestLoadSkillTool_NotFound(t *testing.T) {
	home := t.TempDir()
	store, err := skills.New(home)
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}

	result, err := newLoadSkillTool(store).Execute(context.Background(), map[string]any{"id": "missing"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("Execute: expected IsError for unknown skill")
	}
}
