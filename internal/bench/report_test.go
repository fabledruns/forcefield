package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReport_JSONAndMarkdown(t *testing.T) {
	tmpDir := t.TempDir()

	res := &RunResult{
		RunID:             "test-run-123",
		Timestamp:         "2026-11-11T10:00:00Z",
		ForcefieldVersion: "v1.6",
		GitCommit:         "abc1234",
		Provider:          "ollama",
		Model:             "qwen2.5-coder",
		Tasks: []TaskMetrics{
			{
				TaskID:     "MOD-001",
				Name:       "Slugify",
				Category:   CategorySimpleCodeModifications,
				Difficulty: DifficultyEasy,
				Passed:     true,
				Status:     "passed",
				DurationMS: 1200,
				ToolCalls:  2,
			},
			{
				TaskID:        "DEBUG-001",
				Name:          "Off-by-one",
				Category:      CategoryDebugging,
				Difficulty:    DifficultyMedium,
				Passed:        false,
				Status:        "failed",
				FailureType:   FailureTest,
				FailureReason: "Test failed with secret token sk-test12345678901234567890",
				DurationMS:    2500,
				ToolCalls:     4,
				Validation: ValidationResult{
					Stderr: "FAIL: panic with sk-test12345678901234567890",
				},
			},
		},
	}
	res.Summary = CalculateSummary(res.Tasks)

	jsonPath := filepath.Join(tmpDir, "report.json")
	if err := SaveJSONReport(res, jsonPath); err != nil {
		t.Fatalf("SaveJSONReport failed: %v", err)
	}

	// Verify JSON file parses and matches SchemaVersion
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed JSONReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal generated json report: %v", err)
	}
	if parsed.SchemaVersion != SchemaVersion || parsed.RunID != "test-run-123" {
		t.Fatalf("unexpected json report content: %+v", parsed)
	}

	// Generate and verify Markdown report
	mdContent := GenerateMarkdownReport(res)
	if !strings.Contains(mdContent, "# Forcefield Coding-Agent Benchmark Report") {
		t.Fatal("markdown report missing header")
	}
	if !strings.Contains(mdContent, "MOD-001") || !strings.Contains(mdContent, "DEBUG-001") {
		t.Fatal("markdown report missing task entries")
	}
	// Check secret redaction
	if strings.Contains(mdContent, "sk-test12345678901234567890") {
		t.Fatal("markdown report failed to redact secret key!")
	}
}
