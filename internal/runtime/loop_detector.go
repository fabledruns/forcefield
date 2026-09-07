package runtime

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"forcefield/internal/providers"
)

// repeatedToolExecutionLimit leaves room for a transient retry or a tool
// whose first repeat observes a changing system, while still stopping an
// agent that is replaying the same operation without learning from it.
const repeatedToolExecutionLimit = 3

// loopDetector identifies repeated work by its normalized tool request and
// effective result. Tool IDs are deliberately excluded: providers assign a
// fresh ID for each model turn, so they are not evidence of a changed plan.
//
// The detector is local to one agent run. It does not change scheduling or
// tool execution semantics; it only decides when the runtime should stop
// asking the model for another turn after an unchanged operation completed.
type loopDetector struct {
	calls  map[string]repeatedOperation
	cycles map[string]repeatedOperation
	limit  int
}

type repeatedOperation struct {
	result string
	count  int
}

func newLoopDetector() *loopDetector {
	return &loopDetector{
		calls:  make(map[string]repeatedOperation),
		cycles: make(map[string]repeatedOperation),
		limit:  repeatedToolExecutionLimit,
	}
}

// Observe records one completed tool batch. It reports true only when the
// same request(s) produced effectively the same result often enough that a
// further model/tool cycle would have no evidence of progress.
func (d *loopDetector) Observe(calls []providers.ToolCall, results []ToolResult) bool {
	if d == nil || len(calls) == 0 || len(calls) != len(results) {
		return false
	}

	cycle := make([]string, 0, len(calls))
	for i, call := range calls {
		callKey := normalizedToolCall(call)
		resultKey := normalizedToolResult(results[i])
		if d.observe(d.calls, callKey, resultKey) {
			return true
		}
		cycle = append(cycle, callKey+"\x00"+resultKey)
	}

	// Providers may return independent calls in a different order. Sort the
	// completed request/result pairs so that ordering alone cannot evade the
	// no-progress check.
	sort.Strings(cycle)
	return d.observe(d.cycles, strings.Join(cycle, "\x01"), "")
}

func (d *loopDetector) observe(history map[string]repeatedOperation, key, result string) bool {
	previous, ok := history[key]
	if !ok || previous.result != result {
		history[key] = repeatedOperation{result: result, count: 1}
		return false
	}

	previous.count++
	history[key] = previous
	return previous.count >= d.limit
}

// normalizedToolCall produces a stable representation of a model tool
// request. json.Marshal sorts map keys, including nested map keys, which
// prevents inconsequential JSON argument formatting from defeating the
// detector. Shell command outer whitespace and CRLF differences are also
// normalized because they do not change how the command is invoked.
func normalizedToolCall(call providers.ToolCall) string {
	args := normalizeToolArguments(call.Name, call.Arguments)
	encoded, err := json.Marshal(args)
	if err != nil {
		// Tool arguments are already validated before execution. A malformed
		// value should still have a deterministic key rather than disabling
		// the guard; json's fallback below is intentionally small and local.
		encoded = []byte("<unencodable>")
	}
	return strings.TrimSpace(call.Name) + "\x00" + string(encoded)
}

func normalizeToolArguments(name string, args map[string]any) map[string]any {
	if len(args) == 0 {
		return map[string]any{}
	}
	copyArgs := make(map[string]any, len(args))
	for key, value := range args {
		if (name == "shell" || name == "shell_job") && key == "command" {
			if command, ok := value.(string); ok {
				command = strings.ReplaceAll(command, "\r\n", "\n")
				copyArgs[key] = strings.TrimSpace(command)
				continue
			}
		}
		copyArgs[key] = value
	}
	return copyArgs
}

// normalizedToolResult intentionally ignores timing and attempt count.
// Those fields change even when an operation observed exactly the same
// state. Whitespace-only presentation differences in tool output likewise
// do not constitute meaningful progress.
func normalizedToolResult(result ToolResult) string {
	parts := []string{
		result.Name,
		strings.TrimSpace(strings.Join(strings.Fields(result.Content), " ")),
		strings.TrimSpace(strings.Join(strings.Fields(result.Stdout), " ")),
		strings.TrimSpace(strings.Join(strings.Fields(result.Stderr), " ")),
	}
	if result.Success {
		parts = append(parts, "success")
	} else {
		parts = append(parts, "failure")
	}
	if result.HasExitCode {
		parts = append(parts, "exit="+strconv.Itoa(result.ExitCode))
	}
	return strings.Join(parts, "\x00")
}
