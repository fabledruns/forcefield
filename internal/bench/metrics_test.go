package bench

import (
	"testing"
)

func TestCheckUnexpectedFiles(t *testing.T) {
	expected := []string{"foo.go", "bar/baz.go"}

	// No unexpected files
	if CheckUnexpectedFiles([]string{"foo.go", "bar/baz.go"}, expected) {
		t.Fatal("expected false for exact match, got true")
	}

	// Subset of expected
	if CheckUnexpectedFiles([]string{"foo.go"}, expected) {
		t.Fatal("expected false for subset, got true")
	}

	// Unexpected file modified
	if !CheckUnexpectedFiles([]string{"foo.go", "evil.go"}, expected) {
		t.Fatal("expected true for unexpected file evil.go, got false")
	}

	// No expected files declared (unconstrained)
	if CheckUnexpectedFiles([]string{"anything.go"}, nil) {
		t.Fatal("expected false when expected files is empty, got true")
	}
}
