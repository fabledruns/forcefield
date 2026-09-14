package runtime

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"forcefield/internal/providers"
)

// repeatedToolExecutionLimit bounds consecutive identical operations:
// one repeat may be a retry or a re-check, but three in a row with no
// intervening difference is evidence the agent is stuck.
const repeatedToolExecutionLimit = 3

// loopDetector identifies repeated work by its normalized tool request and
// effective result. Tool IDs are deliberately excluded: providers assign a
// fresh ID for each model turn, so they are not evidence of a changed plan.
//
// Only consecutive identical batches accumulate toward the limit. A batch
// that differs from the previous one — a different tool, different
// arguments, or a different result — is evidence the agent is still
// making progress, so accumulated counts reset. A genuinely stuck agent
// replays the identical operation every turn and still trips the limit.
//
// The detector is local to one agent run. It does not change scheduling or
// tool execution semantics; it only decides when the runtime should stop
// asking the model for another turn after an unchanged operation completed.
type loopDetector struct {
	calls  map[string]repeatedOperation
	cycles map[string]repeatedOperation
	limit  int
	// prev fingerprints the immediately preceding completed batch, so a
	// differing batch can reset the accumulated counts.
	prev string
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

// Observe records one completed tool batch. It reports true only when
// consecutive batches replay effectively the same request(s) with
// effectively the same result often enough that a further model/tool
// cycle would have no evidence of progress. A batch that differs from
// the previous one resets the accumulated counts.
func (d *loopDetector) Observe(calls []providers.ToolCall, results []ToolResult) bool {
	if d == nil || len(calls) == 0 || len(calls) != len(results) {
		return false
	}

	callKeys := make([]string, len(calls))
	resultKeys := make([]string, len(calls))
	pairs := make([]string, len(calls))
	for i, call := range calls {
		callKeys[i] = normalizedToolCall(call)
		resultKeys[i] = normalizedToolResult(results[i])
		pairs[i] = callKeys[i] + "\x00" + resultKeys[i]
	}

	// Providers may return independent calls in a different order. Sort the
	// completed request/result pairs so that ordering alone can neither
	// evade nor trigger the no-progress check.
	sort.Strings(pairs)
	batch := strings.Join(pairs, "\x01")
	if batch != d.prev {
		// The agent did something different since the last batch: a
		// different tool, different arguments, or a different result.
		// That is progress, so prior repetition no longer counts
		// toward stuckness.
		clear(d.calls)
		clear(d.cycles)
		d.prev = batch
	}

	for i := range calls {
		if d.observe(d.calls, callKeys[i], resultKeys[i]) {
			return true
		}
	}
	return d.observe(d.cycles, batch, "")
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
