package bench

import (
	"math"
	"sort"
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
	TotalTasks                 int                      `json:"total_tasks"`
	PassedTasks                int                      `json:"passed_tasks"`
	FailedTasks                int                      `json:"failed_tasks"`
	PassRate                   float64                  `json:"pass_rate"` // 0.0 to 100.0
	TotalDurationMS            int64                    `json:"total_duration_ms"`
	AvgDurationMS              int64                    `json:"avg_duration_ms"`
	LatencyP50MS               int64                    `json:"latency_p50_ms,omitempty"`
	LatencyP95MS               int64                    `json:"latency_p95_ms,omitempty"`
	TotalToolCalls             int                      `json:"total_tool_calls"`
	TotalToolFailures          int                      `json:"total_tool_failures"`
	TotalCommandFailures       int                      `json:"total_command_failures"`
	TotalDelegations           int                      `json:"total_delegations"`
	Tokens                     TokenUsage               `json:"tokens"`
	SlotGuardAdmitted          uint64                   `json:"slot_guard_admitted,omitempty"`
	SlotGuardWaits             uint64                   `json:"slot_guard_waits,omitempty"`
	SlotGuardWaitMS            int64                    `json:"slot_guard_wait_ms,omitempty"`
	SlotGuardDegradedFallbacks uint64                   `json:"slot_guard_degraded_fallbacks,omitempty"`
	AvgPromptPerSecond         float64                  `json:"avg_prompt_per_second,omitempty"`
	AvgPredictedPerSecond      float64                  `json:"avg_predicted_per_second,omitempty"`
	Categories                 map[string]CategoryScore `json:"categories"`
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
	var totalPromptTokens, totalCompletionTokens, totalTokens, totalCachedTokens int
	var tokensReported bool
	var promptTPSum, predTPSum float64
	var promptTPCount, predTPCount int
	durations := make([]int64, 0, len(results))

	for _, r := range results {
		durations = append(durations, r.DurationMS)
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
			totalCachedTokens += r.Tokens.CachedTokens
		}
		if r.SlotGuard != nil {
			s.SlotGuardAdmitted += r.SlotGuard.Admitted
			s.SlotGuardWaits += r.SlotGuard.Waits
			s.SlotGuardWaitMS += r.SlotGuard.WaitMS
			s.SlotGuardDegradedFallbacks += r.SlotGuard.DegradedFallbacks
		}
		if r.Inference != nil {
			if r.Inference.PromptPerSecond > 0 {
				promptTPSum += r.Inference.PromptPerSecond
				promptTPCount++
			}
			if r.Inference.PredictedPerSecond > 0 {
				predTPSum += r.Inference.PredictedPerSecond
				predTPCount++
			}
		}
	}

	s.PassRate = roundTwoDecimals(float64(s.PassedTasks) / float64(s.TotalTasks) * 100.0)
	s.AvgDurationMS = s.TotalDurationMS / int64(s.TotalTasks)
	s.LatencyP50MS = percentileMS(durations, 50)
	s.LatencyP95MS = percentileMS(durations, 95)
	if promptTPCount > 0 {
		s.AvgPromptPerSecond = roundTwoDecimals(promptTPSum / float64(promptTPCount))
	}
	if predTPCount > 0 {
		s.AvgPredictedPerSecond = roundTwoDecimals(predTPSum / float64(predTPCount))
	}

	if tokensReported {
		s.Tokens = TokenUsage{
			Available:        true,
			PromptTokens:     totalPromptTokens,
			CompletionTokens: totalCompletionTokens,
			TotalTokens:      totalTokens,
			CachedTokens:     totalCachedTokens,
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

func percentileMS(values []int64, pct int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (pct*(len(sorted)-1) + 50) / 100
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
