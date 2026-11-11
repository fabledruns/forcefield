package bench

import (
	"testing"
)

func TestCalculateSummary(t *testing.T) {
	metrics := []TaskMetrics{
		{
			TaskID:     "T-1",
			Category:   CategorySimpleCodeModifications,
			Passed:     true,
			DurationMS: 1000,
			ToolCalls:  3,
			Tokens:     TokenUsage{Available: true, PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
		},
		{
			TaskID:       "T-2",
			Category:     CategorySimpleCodeModifications,
			Passed:       false,
			DurationMS:   2000,
			ToolCalls:    5,
			ToolFailures: 1,
			Tokens:       TokenUsage{Available: true, PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300},
		},
		{
			TaskID:     "T-3",
			Category:   CategoryDebugging,
			Passed:     true,
			DurationMS: 1500,
			ToolCalls:  2,
			Tokens:     TokenUsage{Available: true, PromptTokens: 150, CompletionTokens: 50, TotalTokens: 200},
		},
	}

	summary := CalculateSummary(metrics)

	if summary.TotalTasks != 3 {
		t.Fatalf("TotalTasks = %d, want 3", summary.TotalTasks)
	}
	if summary.PassedTasks != 2 {
		t.Fatalf("PassedTasks = %d, want 2", summary.PassedTasks)
	}
	if summary.FailedTasks != 1 {
		t.Fatalf("FailedTasks = %d, want 1", summary.FailedTasks)
	}
	if summary.PassRate != 66.67 {
		t.Fatalf("PassRate = %v, want 66.67", summary.PassRate)
	}
	if summary.TotalDurationMS != 4500 {
		t.Fatalf("TotalDurationMS = %d, want 4500", summary.TotalDurationMS)
	}
	if summary.AvgDurationMS != 1500 {
		t.Fatalf("AvgDurationMS = %d, want 1500", summary.AvgDurationMS)
	}
	if summary.TotalToolCalls != 10 {
		t.Fatalf("TotalToolCalls = %d, want 10", summary.TotalToolCalls)
	}
	if summary.TotalToolFailures != 1 {
		t.Fatalf("TotalToolFailures = %d, want 1", summary.TotalToolFailures)
	}
	if !summary.Tokens.Available || summary.Tokens.TotalTokens != 650 {
		t.Fatalf("TotalTokens = %v, want 650", summary.Tokens)
	}

	// Check category breakdown
	sScore, ok := summary.Categories[CategorySimpleCodeModifications]
	if !ok || sScore.TotalTasks != 2 || sScore.PassedTasks != 1 || sScore.PassRate != 50.0 {
		t.Fatalf("SimpleCodeModifications score mismatch: %+v", sScore)
	}

	dScore, ok := summary.Categories[CategoryDebugging]
	if !ok || dScore.TotalTasks != 1 || dScore.PassedTasks != 1 || dScore.PassRate != 100.0 {
		t.Fatalf("Debugging score mismatch: %+v", dScore)
	}
}

func TestCalculateSummary_Empty(t *testing.T) {
	summary := CalculateSummary(nil)
	if summary.TotalTasks != 0 || summary.PassRate != 0 {
		t.Fatalf("unexpected summary for empty input: %+v", summary)
	}
}
