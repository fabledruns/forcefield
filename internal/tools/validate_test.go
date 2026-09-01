package tools

import "testing"

func TestValidateArgs_RequiredMissing(t *testing.T) {
	def := Definition{
		Name: "read_file",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
	}
	err := ValidateArgs(def, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if _, ok := err.(*ArgumentError); !ok {
		t.Fatalf("error type = %T, want *ArgumentError", err)
	}
}

func TestValidateArgs_WrongType(t *testing.T) {
	def := Definition{
		Name: "read_file",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
	}
	err := ValidateArgs(def, map[string]any{"path": 12345})
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
}

func TestValidateArgs_UnknownField(t *testing.T) {
	def := Definition{
		Name: "read_file",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []string{"path"},
		},
	}
	err := ValidateArgs(def, map[string]any{"path": "x", "extra": "y"})
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestValidateArgs_NumberType(t *testing.T) {
	def := Definition{
		Name: "shell",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":         map[string]any{"type": "string"},
				"timeout_seconds": map[string]any{"type": "number"},
			},
			"required": []string{"command"},
		},
	}
	// Correct number
	if err := ValidateArgs(def, map[string]any{"command": "echo", "timeout_seconds": 30}); err != nil {
		t.Fatalf("unexpected error for correct number: %v", err)
	}
	// Wrong type
	if err := ValidateArgs(def, map[string]any{"command": "echo", "timeout_seconds": "30"}); err == nil {
		t.Fatal("expected error for wrong number type")
	}
}

func TestValidateArgs_EmptySchemaIsPermissive(t *testing.T) {
	def := Definition{
		Name: "test",
		InputSchema: map[string]any{
			"type": "object",
		},
	}
	// Should allow any field when no properties defined
	if err := ValidateArgs(def, map[string]any{"value": "x", "extra": 123}); err != nil {
		t.Fatalf("empty schema should be permissive, got %v", err)
	}
}

func TestValidateArgs_OptionalStringWrongTypeBeforePermission(t *testing.T) {
	// This is the dangerous default case: {"path":12345} should not silently become "."
	def := Definition{
		Name: "list_files",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
		},
	}
	err := ValidateArgs(def, map[string]any{"path": 12345})
	if err == nil {
		t.Fatal("expected error for path wrong type, to prevent silent default to '.'")
	}
}
