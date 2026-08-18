package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// toolRecord carries a tool call's structured details for the lifetime of
// its activity line, so the compact one-line summary can be expanded
// (ctrl+e) into full command/input/output details without re-fetching
// anything.
type toolRecord struct {
	name      string
	args      map[string]any
	finished  bool
	eventType runtime.EventType
	content   string
	stdout    string
	stderr    string
	exitCode  int
	hasExit   bool
	duration  time.Duration
	err       string
	expanded  bool
}

// maxExpandedOutputLines bounds how many lines of stdout/stderr/content the
// expanded tool view shows; anything longer is collapsed with a count of
// the hidden lines so a huge tool result can't flood the transcript.
const maxExpandedOutputLines = 40

func formatToolStart(call *providers.ToolCall) string {
	if call == nil {
		return IconRunning.String() + " Running tool"
	}

	if detail := usefulToolArgument(call.Arguments); detail != "" {
		return fmt.Sprintf("%s Running %s %s", IconRunning, call.Name, detail)
	}
	return fmt.Sprintf("%s Running %s", IconRunning, call.Name)
}

// formatToolProgress renders a single streamed output line (e.g. one line
// of shell stdout/stderr) as a live-updating status line.
func formatToolProgress(progress *runtime.ToolProgress) string {
	if progress == nil {
		return ""
	}
	line := shortResult(progress.Data)
	if line == "" {
		return fmt.Sprintf("%s Running %s…", IconRunning, progress.Name)
	}
	return fmt.Sprintf("%s Running %s %s %s", IconRunning, progress.Name, IconPipe, line)
}

func formatToolFinish(result *runtime.ToolResult, eventType runtime.EventType) string {
	if result == nil {
		return IconFailure.String() + " Tool failed"
	}

	if eventType == runtime.EventToolCancelled {
		message := fmt.Sprintf("%s %s cancelled", IconCancel, result.Name)
		if summary := shortResult(result.Content); summary != "" {
			return message + ": " + summary
		}
		return message
	}

	if !result.Success {
		message := fmt.Sprintf("%s %s failed", IconFailure, result.Name)
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
			message = fmt.Sprintf("%s Found %d entries", IconSuccess, count)
		}
	case "read_file":
		if path, ok := result.Arguments["path"].(string); ok && path != "" {
			message = fmt.Sprintf("%s Read %s", IconSuccess, path)
		}
	case "write_file":
		if path, ok := result.Arguments["path"].(string); ok && path != "" {
			message = fmt.Sprintf("%s Wrote %s", IconSuccess, path)
		}
	}
	if message == "" {
		message = fmt.Sprintf("%s %s completed", IconSuccess, result.Name)
	}
	return message + durationSuffix(result.Duration)
}

// formatToolDetails renders the expanded view of a finished (or running)
// tool call: its input, working directory, exit code, duration, and
// bounded stdout/stderr/result sections.
func formatToolDetails(t *toolRecord, width int) string {
	var b strings.Builder

	appendSection := func(label, value string) {
		value = strings.TrimRight(value, "\n")
		if value == "" {
			return
		}
		fmt.Fprintf(&b, "\n  %s", label)
		for _, line := range clampLines(value, maxExpandedOutputLines) {
			b.WriteString("\n  │ " + line)
		}
	}

	if t.args != nil {
		for _, key := range sortedArgKeys(t.args) {
			if s, ok := t.args[key].(string); ok && s != "" {
				appendSection(key+":", s)
			}
		}
	}
	if t.hasExit {
		fmt.Fprintf(&b, "\n  exit code: %d", t.exitCode)
	}
	if t.duration > 0 {
		fmt.Fprintf(&b, "\n  duration: %s", t.duration.Round(time.Millisecond))
	}
	if t.err != "" {
		appendSection("error:", t.err)
	}
	appendSection("stdout:", t.stdout)
	appendSection("stderr:", t.stderr)
	if t.content != "" && t.content != t.stdout {
		appendSection("result:", t.content)
	}
	if b.Len() == 0 {
		return "\n  (no details)"
	}
	return b.String()
}

func sortedArgKeys(args map[string]any) []string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// clampLines limits value to max lines, marking how many were hidden.
func clampLines(value string, max int) []string {
	lines := strings.Split(value, "\n")
	if len(lines) <= max {
		return lines
	}
	hidden := len(lines) - max
	out := append([]string{}, lines[:max]...)
	out = append(out, fmt.Sprintf("… (+%d more lines)", hidden))
	return out
}

func usefulToolArgument(args map[string]any) string {
	for _, key := range []string{"path", "command", "cmd", "query", "url"} {
		value, ok := args[key].(string)
		if !ok || value == "" {
			continue
		}
		if key == "command" || key == "cmd" {
			return quoteCommand(value)
		}
		return value
	}
	return ""
}

// quoteCommand renders a shell command for the compact one-line tool
// status. A multiline command shows its first line quoted plus a count of
// the remaining lines - never the two-character escape \n, which reads as
// if the command itself contained a literal backslash-n.
func quoteCommand(command string) string {
	if !strings.ContainsAny(command, "\n\r") {
		return strconv.Quote(command)
	}
	normalized := strings.ReplaceAll(command, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	return strconv.Quote(lines[0]) + fmt.Sprintf(" (+%d more lines)", len(lines)-1)
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

// shortResult collapses a result to one summary line, truncating on rune
// boundaries so multi-byte characters are never split into mojibake.
func shortResult(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	const limit = 120
	if len(value) <= limit {
		return value
	}
	truncated := []rune(value)[:limit-1]
	return string(truncated) + "…"
}

func durationSuffix(duration time.Duration) string {
	// Tool calls that finish instantly do not need visual noise. Duration is
	// still available on the structured event for richer consumers.
	if duration < 100*time.Millisecond {
		return ""
	}
	return fmt.Sprintf(" (%s)", duration.Round(10*time.Millisecond))
}
