package bench

import (
	"math"
)

// CategoryScore summarizes metrics within a single benchmark category.
type CategoryScore struct {
	TotalTasks      int     `json:"total_tasks"`
	PassedTasks     int     `json:"passed_tasks"`
	FailedTasks     int     `json:"failed_tasks"`
	PassRate        float64 `json:"pass_rate"` // 0.0 to 100.0
	DurationMS      int64   `json:"duration_ms"`
	AvgDurationMS   int64   `json:"avg_duration_ms"`
	ToolCalls       int     `json:"tool_calls"`
	ToolFailures    int     `json:"tool_failures"`
	CommandFailures int     `json:"command_failures"`
	PromptTokens    int     `json:"prompt_tokens,omitempty"`
	CompleteTokens  int     `json:"complete_tokens,omitempty"`
	TotalTokens     int     `json:"total_tokens,omitempty"`
	HasTokens       bool    `json:"has_tokens"`
}

// Summary summarizes the entire benchmark run.
type Summary struct {
	TotalTasks           int                      `json:"total_tasks"`
	PassedTasks          int                      `json:"passed_tasks"`
	FailedTasks          int                      `json:"failed_tasks"`
	PassRate             float64                  `json:"pass_rate"` // 0.0 to 100.0
	TotalDurationMS      int64                    `json:"total_duration_ms"`
	AvgDurationMS        int64                    `json:"avg_duration_ms"`
	TotalToolCalls       int                      `json:"total_tool_calls"`
	TotalToolFailures    int                      `json:"total_tool_failures"`
	TotalCommandFailures int                      `json:"total_command_failures"`
	TotalDelegations     int                      `json:"total_delegations"`
	Tokens               TokenUsage               `json:"tokens"`
	Categories           map[string]CategoryScore `json:"categories"`
}

// CalculateSummary aggregates task metrics into a structured benchmark summary.
func CalculateSummary(results []TaskMetrics) Summary {
	s := Summary{
		TotalTasks: len(results),
		Categories: make(map[string]CategoryScore),
	}

	if len(results) == 0 {
		return s
	}

	catMap := make(map[string][]TaskMetrics)
	var totalPromptTokens, totalCompletionTokens, totalTokens int
	var tokensReported bool

	for _, r := range results {
		catMap[r.Category] = append(catMap[r.Category], r)
		if r.Passed {
			s.PassedTasks++
		} else {
			s.FailedTasks++
		}
		s.TotalDurationMS += r.DurationMS
		s.TotalToolCalls += r.ToolCalls
		s.TotalToolFailures += r.ToolFailures
		s.TotalCommandFailures += r.CommandFailures
		s.TotalDelegations += r.Delegations

		if r.Tokens.Available {
			tokensReported = true
			totalPromptTokens += r.Tokens.PromptTokens
			totalCompletionTokens += r.Tokens.CompletionTokens
			totalTokens += r.Tokens.TotalTokens
		}
	}

	s.PassRate = roundTwoDecimals(float64(s.PassedTasks) / float64(s.TotalTasks) * 100.0)
	s.AvgDurationMS = s.TotalDurationMS / int64(s.TotalTasks)

	if tokensReported {
		s.Tokens = TokenUsage{
			Available:        true,
			PromptTokens:     totalPromptTokens,
			CompletionTokens: totalCompletionTokens,
			TotalTokens:      totalTokens,
		}
	} else {
		s.Tokens = TokenUsage{
			Available: false,
		}
	}

	// Calculate per-category breakdown
	for cat, list := range catMap {
		cs := CategoryScore{
			TotalTasks: len(list),
		}
		var catPrompt, catCompl, catTot int
		var catHasTokens bool

		for _, item := range list {
			if item.Passed {
				cs.PassedTasks++
			} else {
				cs.FailedTasks++
			}
			cs.DurationMS += item.DurationMS
			cs.ToolCalls += item.ToolCalls
			cs.ToolFailures += item.ToolFailures
			cs.CommandFailures += item.CommandFailures

			if item.Tokens.Available {
				catHasTokens = true
				catPrompt += item.Tokens.PromptTokens
				catCompl += item.Tokens.CompletionTokens
				catTot += item.Tokens.TotalTokens
			}
		}

		if cs.TotalTasks > 0 {
			cs.PassRate = roundTwoDecimals(float64(cs.PassedTasks) / float64(cs.TotalTasks) * 100.0)
			cs.AvgDurationMS = cs.DurationMS / int64(cs.TotalTasks)
		}

		if catHasTokens {
			cs.HasTokens = true
			cs.PromptTokens = catPrompt
			cs.CompleteTokens = catCompl
			cs.TotalTokens = catTot
		}
		s.Categories[cat] = cs
	}

	return s
}

func roundTwoDecimals(val float64) float64 {
	return math.Round(val*100) / 100
}
