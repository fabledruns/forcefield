package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/redact"
	"forcefield/internal/tools"
)

// Adversarial secrets/canary tests. Synthetic canary values embed an FFSEC
// marker INSIDE the secret body: the marker can only disappear together
// with the whole secret, so scanning every parent-visible surface for the
// verbatim marker catches partial redactions, split artifacts, and future
// output paths someone forgot to scrub. No real credentials, no network,
// no provider — everything runs against scripted model turns.

// canaryMarker is embedded inside every canary secret body. It must never
// appear verbatim in any surface a parent consumer can reach.
const canaryMarker = "FFSEC"

// childShapeCanaries returns one synthetic credential per known redaction
// shape (see internal/redact). Each lives on its own line with its
// prefilter keyword so every pattern gets a real chance to match.
func childShapeCanaries() []string {
	return []string{
		"sk-FFSECabcdefghij1234567890",
		"gsk_FFSECabcdefghij1234567890",
		"api_key=FFSEC-probe-value-3921",
		"bearer FFSECabcdefghijklmnopqrstuvwxyz",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBCAQEAFFSECabcdefghij1234567890\n-----END RSA PRIVATE KEY-----",
		"ghp_FFSECabcdefghij1234567890abcdefghij1234567890",
		"xoxb-FFSEC-abc123def456",
		"AKIAFFSEC11111111111",
		"AIzaFFSECabcdefghijklmnopqrstuvwxyz0123456789",
		"password=FFSEC-touchy-value-158344",
	}
}

// messageRecordingProvider records every provider-facing message window so
// tests can assert on what the MODEL actually sees next turn (the
// truncated, scrubbed, fenced view), not just what events carried.
type messageRecordingProvider struct {
	inner providers.ModelProvider

	mu    sync.Mutex
	calls [][]providers.Message
}

func (p *messageRecordingProvider) StreamChat(ctx context.Context, messages []providers.Message, defs []tools.Definition) (<-chan providers.StreamEvent, error) {
	p.mu.Lock()
	p.calls = append(p.calls, append([]providers.Message(nil), messages...))
	p.mu.Unlock()
	return p.inner.StreamChat(ctx, messages, defs)
}

// messageTexts flattens all recorded provider windows into one scan
// surface. Assistant tool calls are included so attack strings hidden in
// model-generated arguments are scanned too.
func (p *messageRecordingProvider) messageTexts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for i, call := range p.calls {
		for j, m := range call {
			out = append(out, fmt.Sprintf("call %d message %d %s: %s", i, j, m.Role, m.Content))
			for _, tc := range m.ToolCalls {
				out = append(out, fmt.Sprintf("call %d message %d tool_call: %s", i, j, renderAdversarialToolCall(tc)))
			}
		}
	}
	return out
}

// renderAdversarialToolCall flattens one tool call (id, name, arguments)
// into one scan surface.
func renderAdversarialToolCall(tc providers.ToolCall) string {
	return fmt.Sprintf("%s %s %+v", tc.ID, tc.Name, tc.Arguments)
}

// drainAllEventsForTest consumes the whole event stream and retains every
// event in arrival order — the full audit surface for canary scanning.
// The runtime always terminates the stream, so this drain is bounded.
func drainAllEventsForTest(t *testing.T, events <-chan Event) []Event {
	t.Helper()
	var drained []Event
	for ev := range events {
		drained = append(drained, ev)
	}
	return drained
}

// eventCanarySurfaces enumerates every string surface one event carries.
// Keep this exhaustive with the Event struct: a new field that can carry
// model-controlled text must be added here.
func eventCanarySurfaces(ev Event) []string {
	var out []string
	out = append(out, ev.Text, ev.Thinking)
	if ev.ToolCall != nil {
		out = append(out, renderAdversarialToolCall(*ev.ToolCall))
	}
	if ev.ToolProgress != nil {
		out = append(out, ev.ToolProgress.Name, ev.ToolProgress.Data)
	}
	if ev.ToolResult != nil {
		out = append(out, ev.ToolResult.Name, ev.ToolResult.Content, ev.ToolResult.Stdout, ev.ToolResult.Stderr)
		if ev.ToolResult.Err != nil {
			out = append(out, ev.ToolResult.Err.Error())
		}
	}
	if ev.Response != nil {
		out = append(out, ev.Response.Content)
		for _, tc := range ev.Response.ToolCalls {
			out = append(out, renderAdversarialToolCall(tc))
		}
	}
	if ev.TaskState != nil {
		// The snapshot carries goal/plan/discoveries/blockers strings the
		// run could have filled from child-derived material; render the
		// whole struct so no field escapes enumeration.
		out = append(out, fmt.Sprintf("%+v", *ev.TaskState))
	}
	if ev.Err != nil {
		out = append(out, ev.Err.Error())
	}
	return out
}

// assertNoCanaryLeak fails if any scan surface contains any canary literal,
// or the embedded marker. The marker catches partial redactions: every
// canary embeds FFSEC inside the secret body, so a scrub failure that
// keeps even a fragment of the secret keeps the marker too.
func assertNoCanaryLeak(t *testing.T, label string, surfaces []string, canaries []string) {
	t.Helper()
	for _, s := range surfaces {
		for _, c := range canaries {
			if strings.Contains(s, c) {
				t.Fatalf("%s: canary leaked verbatim on surface %.80q (canary %.40q)", label, s, c)
			}
		}
		if strings.Contains(s, canaryMarker) {
			t.Fatalf("%s: canary marker %q survives on surface %.120q — part of a secret escaped scrubbing", label, canaryMarker, s)
		}
	}
}

// delegateReportContentForTest returns the parent-visible content of the
// delegate tool's result event, failing if the call never finished.
// Successful delegate results emit EventToolFinish; a child failure
// (IsError composition) emits EventToolFailed — both are result-bearing
// terminal events for the parent's call.
func delegateReportContentForTest(t *testing.T, drained []Event) string {
	t.Helper()
	for _, ev := range drained {
		if ev.Type == EventToolFinish || ev.Type == EventToolFailed {
			if ev.ToolResult != nil && ev.ToolResult.Name == "delegate" {
				return ev.ToolResult.Content
			}
		}
	}
	t.Fatal("delegate tool result never reached the parent transcript")
	return ""
}

// toolMessagesForTest returns the recorded model-visible messages for one
// tool name across all provider calls.
func toolMessagesForTest(t *testing.T, p *messageRecordingProvider, name string) []providers.Message {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []providers.Message
	for _, call := range p.calls {
		for _, m := range call {
			if m.Role == providers.ToolRole && m.Name == name {
				out = append(out, m)
			}
		}
	}
	return out
}

// scanAllAdversarialSurfaces collects the drained events and the recorded
// provider windows into one scan surface list.
func scanAllAdversarialSurfaces(drained []Event, provider *messageRecordingProvider) []string {
	var surfaces []string
	for _, ev := range drained {
		surfaces = append(surfaces, eventCanarySurfaces(ev)...)
	}
	surfaces = append(surfaces, provider.messageTexts()...)
	return surfaces
}

// TestAdversarialCanarySecretsNeverSurviveDelegation [R]: a child's final
// report containing one synthetic credential per known shape is redacted
// before anything reaches the parent transcript — and before anything
// reaches the parent's NEXT provider request. Whole-model-visibility is
// the boundary: the report the child produced exists only inside the
// delegation, in scrubbed form.
func TestAdversarialCanarySecretsNeverSurviveDelegation(t *testing.T) {
	canaries := childShapeCanaries()
	provider := &messageRecordingProvider{inner: &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-canary-delegate", "researcher", "canary sweep")}},
				{Done: true},
			},
			{{Text: "parent coordinated the sweep.", Done: true}},
		},
		childTurns: map[string][][]providers.StreamEvent{
			"canary sweep": oneTextTurn(strings.Join(append([]string{
				"benign-passthrough-anchor-1397 stayed as-is",
			}, canaries...), "\n")),
		},
	}}
	r, _, _ := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), nil,
		config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive})

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "run the sweep",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	assertNoCanaryLeak(t, "full parent surface", scanAllAdversarialSurfaces(drained, provider), canaries)

	// Affirmative evidence, not just absence: every canary leaves its own
	// explicit [redacted ...] marker in the report, and benign text
	// survives intact — the scrub is surgical, not a blackout.
	content := delegateReportContentForTest(t, drained)
	if n := strings.Count(content, "[redacted"); n < len(canaries) {
		t.Fatalf("expected one redaction marker per canary (>= %d) in the delegate report, got %d:\n%.400q", len(canaries), n, content)
	}
	if !strings.Contains(content, "benign-passthrough-anchor-1397 stayed as-is") {
		t.Fatalf("scrub removed benign report text (overzealous):\n%.400q", content)
	}

	// The same report enters the parent's next provider request, truncated
	// and fenced, still fully scrubbed: the boundary holds at the provider
	// surface, not only at the transcript surface.
	toolMsgs := toolMessagesForTest(t, provider, "delegate")
	if len(toolMsgs) == 0 {
		t.Fatal("delegate tool result never reached the parent's provider window")
	}
	if m := strings.Count(toolMsgs[0].Content, "[redacted"); m < len(canaries) {
		t.Fatalf("provider-visible report lost redaction markers: %d of at least %d\n%.400q", m, len(canaries), toolMsgs[0].Content)
	}
}

// TestAdversarialExactValueRegistryCanary [R]: a credential value
// registered through redact.AddSecret is redacted even though its text
// contains no known shape and no prefilter keyword — the exact-value
// registry is deliberately independent of the keyword fast path, because
// real leaks rarely announce themselves.
func TestAdversarialExactValueRegistryCanary(t *testing.T) {
	exact := "FFSEC-exact-7b2e9d-channel-value2026"
	redact.AddSecret(exact)
	t.Cleanup(redact.ResetSecrets)

	provider := &messageRecordingProvider{inner: &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-exact-delegate", "researcher", "exact value pass")}},
				{Done: true},
			},
			{{Text: "parent acknowledged the report.", Done: true}},
		},
		childTurns: map[string][][]providers.StreamEvent{
			// The surrounding line carries no redact keyword at all: only
			// the exact-value registry can catch this.
			"exact value pass": oneTextTurn("the operator's private per-run salt is " + exact + " recorded at a quiet hour."),
		},
	}}
	r, _, _ := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), nil,
		config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive})

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "carry the salt",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	assertNoCanaryLeak(t, "exact-value surface", scanAllAdversarialSurfaces(drained, provider), []string{exact})

	content := delegateReportContentForTest(t, drained)
	if !strings.Contains(content, "salt is [redacted] recorded") {
		t.Fatalf("exact-value registry did not replace the registered value around it:\n%.300q", content)
	}
}

// TestAdversarialChildFailureErrorTextCanary [R]: a child provider stream
// that fails mid-turn with a credential-shaped error string surfaces to
// the parent only as scrubbed text inside the delegated report, and the
// parent run continues afterward.
//
// Note the turn layer already scrubs provider errors (streamOneTurn's
// redact.ScrubError) and the composition layer scrubs again
// (composeDelegateResult → scrubbedContent): this test pins the property
// (no leak), not the layering. Defense-in-depth at two sites is the
// design; see the M2 note in docs/OrchestrationTesting.md.
func TestAdversarialChildFailureErrorTextCanary(t *testing.T) {
	errCanary := "ghp_FFSECabcdefghij1234567890abcdefghij1234567890"
	provider := &messageRecordingProvider{inner: &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-err-delegate", "researcher", "sick child report")}},
				{Done: true},
			},
			{{Text: "parent handled the failure.", Done: true}},
		},
		childTurns: map[string][][]providers.StreamEvent{
			"sick child report": {{
				{Err: fmt.Errorf("child provider exploded: %s", errCanary)},
			}},
		},
	}}
	r, _, _ := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), nil,
		config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive})

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "watch the sick child fail",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	assertNoCanaryLeak(t, "child-failure surface", scanAllAdversarialSurfaces(drained, provider), []string{errCanary})

	content := delegateReportContentForTest(t, drained)
	if !strings.Contains(content, "child run failed: ") {
		t.Fatalf("child failure not reported honestly:\n%.300q", content)
	}
	if !strings.Contains(content, "[redacted") {
		t.Fatalf("child-failure error text carries no redaction marker:\n%.300q", content)
	}

	// The parent survives a scrubbed child failure: the run continues to
	// its own terminal event instead of wedging on the child's error.
	finalSeen := false
	for _, ev := range drained {
		if ev.Type == EventDone && ev.Response != nil && strings.Contains(ev.Response.Content, "parent handled the failure.") {
			finalSeen = true
		}
	}
	if !finalSeen {
		t.Fatal("parent run did not complete after the child failure")
	}
}
