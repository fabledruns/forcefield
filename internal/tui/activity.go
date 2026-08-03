package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

func formatToolStart(call *providers.ToolCall) string {
	if call == nil {
		return "Running tool"
	}

	if detail := usefulToolArgument(call.Arguments); detail != "" {
		return fmt.Sprintf("Running %s %s", call.Name, detail)
	}
	return fmt.Sprintf("Running %s", call.Name)
}

// formatToolProgress renders a single streamed output line (e.g. one line
// of shell stdout/stderr) as a live-updating status line.
func formatToolProgress(progress *runtime.ToolProgress) string {
	if progress == nil {
		return ""
	}
	line := shortResult(progress.Data)
	if line == "" {
		return fmt.Sprintf("Running %s…", progress.Name)
	}
	return fmt.Sprintf("Running %s │ %s", progress.Name, line)
}

func formatToolFinish(result *runtime.ToolResult, eventType runtime.EventType) string {
	if result == nil {
		return "✕ Tool failed"
	}

	if eventType == runtime.EventToolCancelled {
		message := fmt.Sprintf("⊘ %s cancelled", result.Name)
		if summary := shortResult(result.Content); summary != "" {
			return message + ": " + summary
		}
		return message
	}

	if !result.Success {
		message := fmt.Sprintf("✕ %s failed", result.Name)
		if result.Attempt > 1 {
			message += fmt.Sprintf(" (after %d attempts)", result.Attempt)
		}
		if result.Err != nil {
			return message + ": " + result.Err.Error()
		}
		if summary := shortResult(result.Content); summary != "" {
			return message + ": " + summary
		}
		return message
	}

	var message string
	switch result.Name {
	case "list_files":
		if count := nonEmptyLines(result.Content); count > 0 {
			message = fmt.Sprintf("✓ Found %d entries", count)
		}
	case "read_file":
		if path, ok := result.Arguments["path"].(string); ok && path != "" {
			message = fmt.Sprintf("✓ Read %s", path)
		}
	case "write_file":
		if path, ok := result.Arguments["path"].(string); ok && path != "" {
			message = fmt.Sprintf("✓ Wrote %s", path)
		}
	}
	if message == "" {
		message = fmt.Sprintf("✓ %s completed", result.Name)
	}
	return message + durationSuffix(result.Duration)
}

func usefulToolArgument(args map[string]any) string {
	for _, key := range []string{"path", "command", "cmd", "query", "url"} {
		value, ok := args[key].(string)
		if !ok || value == "" {
			continue
		}
		if key == "command" || key == "cmd" {
			return strconv.Quote(value)
		}
		return value
	}
	return ""
}

func nonEmptyLines(value string) int {
	count := 0
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func shortResult(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	const limit = 120
	if len(value) > limit {
		return value[:limit-1] + "…"
	}
	return value
}

func durationSuffix(duration time.Duration) string {
	// Tool calls that finish instantly do not need visual noise. Duration is
	// still available on the structured event for richer consumers.
	if duration < 100*time.Millisecond {
		return ""
	}
	return fmt.Sprintf(" (%s)", duration.Round(10*time.Millisecond))
}
