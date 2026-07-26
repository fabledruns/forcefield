package tools

import "testing"

func TestStringArg(t *testing.T) {
	got, err := StringArg(map[string]any{"path": "/tmp/x"}, "path")
	if err != nil {
		t.Fatalf("StringArg() unexpected error: %v", err)
	}
	if got != "/tmp/x" {
		t.Fatalf("StringArg() = %q, want %q", got, "/tmp/x")
	}
}

func TestStringArg_Missing(t *testing.T) {
	_, err := StringArg(map[string]any{}, "path")
	if err == nil {
		t.Fatal("StringArg() with missing key = nil error, want an error")
	}
}

func TestStringArg_WrongType(t *testing.T) {
	_, err := StringArg(map[string]any{"path": 42}, "path")
	if err == nil {
		t.Fatal("StringArg() with wrong type = nil error, want an error")
	}
}

func TestOptionalStringArg_Present(t *testing.T) {
	got := OptionalStringArg(map[string]any{"path": "here"}, "path", "default")
	if got != "here" {
		t.Fatalf("OptionalStringArg() = %q, want %q", got, "here")
	}
}

func TestOptionalStringArg_Missing(t *testing.T) {
	got := OptionalStringArg(map[string]any{}, "path", "default")
	if got != "default" {
		t.Fatalf("OptionalStringArg() = %q, want %q", got, "default")
	}
}

func TestOptionalStringArg_WrongTypeFallsBackToDefault(t *testing.T) {
	got := OptionalStringArg(map[string]any{"path": 42}, "path", "default")
	if got != "default" {
		t.Fatalf("OptionalStringArg() = %q, want %q", got, "default")
	}
}
