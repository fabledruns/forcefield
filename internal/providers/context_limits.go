package providers

import (
	"sort"
	"strings"
)

// DefaultReserveTokens is the space the runtime reserves for the next
// model response when no better estimate exists. It matches the smallest
// MaxOutputTokens any built-in adapter reports, so the budget never
// assumes more room than an unknown model can actually emit.
const DefaultReserveTokens = 4096

// knownContextWindows maps lowercase model-ID substrings to approximate
// context windows in tokens. Entries are ordered longest-first at lookup
// time so specific IDs win over family prefixes.
//
// These are conservative, publicly documented sizes — not live provider
// data. Unknown models report 0 (unknown) and the runtime falls back to
// message-count windowing. Never invent a value: leave unknown models at
// 0 rather than guessing.
var knownContextWindows = map[string]int{
	"gemini-2.5-pro":    1048576,
	"gemini-2.5-flash":  1048576,
	"deepseek-v4":       1048576,
	"claude-sonnet-4-5": 200000,
	"claude-haiku-4-5":  200000,
	"claude-opus-4-5":   200000,
	"gpt-4o-mini":       128000,
	"gpt-4o":            128000,
	"grok-3-mini":       131072,
	"grok-3":            131072,
	"llama-3.3":         128000,
	"mistral-large":     128000,
	"mistral-small":     128000,
	"glm-5":             200000,
	"qwen3":             131072,
	"local-model":       32768,
}

// knownMaxOutput maps lowercase model-ID substrings to approximate
// single-turn output limits. The Anthropic family requires an explicit
// max_tokens per request (currently 8192 in the adapter); everything
// else defaults to DefaultReserveTokens.
var knownMaxOutput = map[string]int{
	"claude":   8192,
	"gemini":   8192,
	"gpt-4o":   4096,
	"gpt-5":    8192,
	"grok-3":   4096,
	"glm-5":    8192,
	"deepseek": 8192,
}

// ContextLimitsForModel returns the approximate (contextWindow,
// maxOutputTokens) for a model ID. Either value may be 0 meaning
// unknown — callers must fall back rather than assume. Matching is
// case-insensitive substring on the longest known key first.
func ContextLimitsForModel(modelID string) (contextWindow, maxOutput int) {
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if lower == "" {
		return 0, 0
	}
	for _, key := range sortedKeys(knownContextWindows) {
		if strings.Contains(lower, key) {
			contextWindow = knownContextWindows[key]
			break
		}
	}
	for _, key := range sortedKeys(knownMaxOutput) {
		if strings.Contains(lower, key) {
			maxOutput = knownMaxOutput[key]
			break
		}
	}
	return contextWindow, maxOutput
}

// CapabilitiesForModel reports the transport's base capabilities with
// the model's approximate context/output limits filled in when known.
// Unknown models keep 0 (unknown) rather than a fabricated value.
func CapabilitiesForModel(protocolType, modelID string) Capabilities {
	caps := CapabilitiesFor(protocolType)
	window, maxOut := ContextLimitsForModel(modelID)
	if window > 0 {
		caps.ContextWindow = window
	}
	if maxOut > 0 {
		caps.MaxOutputTokens = maxOut
	} else if caps.MaxOutputTokens == 0 {
		// Transports that do not name a default still get the
		// conservative reserve so callers never assume unbounded room.
		caps.MaxOutputTokens = DefaultReserveTokens
	}
	return caps
}

// ResolveCapabilities negotiates the effective capabilities for one
// provider instance and model: transport features come from the
// instance itself (when it reports them), while context/output limits
// fall back to the static table for models the transport does not
// describe. The runtime decides tool definitions, parallelism, and
// context budgets from this — never from provider-type branches.
func ResolveCapabilities(p ModelProvider, modelID string) Capabilities {
	var caps Capabilities
	if cp, ok := p.(CapabilitiesProvider); ok && cp != nil {
		caps = cp.Capabilities()
	}
	window, maxOut := ContextLimitsForModel(modelID)
	if caps.ContextWindow <= 0 && window > 0 {
		caps.ContextWindow = window
	}
	if caps.MaxOutputTokens <= 0 {
		if maxOut > 0 {
			caps.MaxOutputTokens = maxOut
		} else {
			caps.MaxOutputTokens = DefaultReserveTokens
		}
	}
	return caps
}

// ReportsCapabilities reports whether p describes its own capabilities.
// Runtimes use it to distinguish "known to lack a feature" from
// "too old to say": unreported providers keep historical behavior.
func ReportsCapabilities(p ModelProvider) bool {
	if p == nil {
		return false
	}
	_, ok := p.(CapabilitiesProvider)
	return ok
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return keys
}
