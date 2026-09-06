package security

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

// TestSecretScan_FindingOverrideBoundsOutput pins that a configured
// finding bound replaces the 50-finding default, with the marker naming
// the live bound and structured metadata attached.
func TestSecretScan_FindingOverrideBoundsOutput(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("token = \"abcdefgh12345678\"\n")
	}
	s := NewSecretScan()
	s.SetLimits(tools.Limits{MaxLines: 3})

	result, err := s.Execute(context.Background(), map[string]any{"text": b.String()})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "[truncated at 3 findings]") {
		t.Errorf("content lacks override marker:\n%.500s", result.Content)
	}
	if result.Metadata == nil || result.Metadata["truncated"] != true || result.Metadata["finding_limit"] != 3 {
		t.Errorf("Metadata = %v, want truncation record with limit 3", result.Metadata)
	}
}

// TestSecretScan_DefaultFindingBoundUnchanged pins the default path.
func TestSecretScan_DefaultFindingBoundUnchanged(t *testing.T) {
	if got := NewSecretScan().ToolLimits().MaxLines; got != tools.DefaultSecretMaxFindings {
		t.Errorf("default MaxLines = %d, want %d", got, tools.DefaultSecretMaxFindings)
	}
	s := NewSecretScan()
	result, err := s.Execute(context.Background(), map[string]any{"text": "api_key = \"abcdefgh12345678\"\n"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("IsError = true: %s", result.Content)
	}
	if strings.Contains(result.Content, "[truncated at") {
		t.Errorf("single finding has a marker: %q", result.Content)
	}
	if result.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", result.Metadata)
	}
}
