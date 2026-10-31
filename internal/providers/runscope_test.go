package providers

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

// runScopeCase describes one built-in adapter for the cloning contract
// tests: how to construct it, which optional features it carries, and
// nothing else. Every case must produce a provider with a working Clone
// that satisfies the RunScopedProvider contract.
type runScopeCase struct {
	name         string
	hasReasoning bool
	hasClient    bool
	mk           func(t *testing.T) ModelProvider
}

// specOf extracts the immutable configuration of a built-in adapter so
// clone tests can compare configuration wholesale.
func specOf(p ModelProvider) Spec {
	switch t := p.(type) {
	case *OpenAICompatible:
		return t.spec
	case *AnthropicProvider:
		return t.spec
	case *GeminiProvider:
		return t.spec
	case *OpenAIResponses:
		return t.spec
	case *OllamaProvider:
		// Ollama stores its configuration in exported fields rather than
		// a spec; compare the equivalent shape.
		return Spec{Type: "ollama", BaseURL: t.Endpoint, Model: t.Model}
	case *VertexProvider:
		return t.spec
	case *BedrockProvider:
		return t.spec
	case *AzureProvider:
		return t.spec
	case *OpenCodeRouter:
		return t.spec
	default:
		return Spec{}
	}
}

// privateGate returns the single-request gate backing adapter requests.
// Wrappers route into their inner adapters, which is exactly where the
// effective gate lives.
func privateGate(p ModelProvider) *requestGate {
	switch t := p.(type) {
	case *OpenAICompatible:
		return t.gate
	case *AnthropicProvider:
		return t.gate
	case *GeminiProvider:
		return t.gate
	case *OpenAIResponses:
		return t.gate
	case *OllamaProvider:
		return t.gate
	case *VertexProvider:
		return t.gate
	case *BedrockProvider:
		return t.gate
	case *AzureProvider:
		return privateGate(t.inner)
	case *OpenCodeRouter:
		return privateGate(t.inner)
	default:
		return nil
	}
}

// privateClient returns the shared HTTP client, matching gate routing.
func privateClient(p ModelProvider) *http.Client {
	switch t := p.(type) {
	case *OpenAICompatible:
		return t.client
	case *AnthropicProvider:
		return t.client
	case *GeminiProvider:
		return t.client
	case *OpenAIResponses:
		return t.client
	case *OllamaProvider:
		return t.client
	case *VertexProvider:
		return t.client
	case *BedrockProvider:
		return nil // SDK clients are built per request from the seam
	case *AzureProvider:
		return privateClient(t.inner)
	case *OpenCodeRouter:
		return privateClient(t.inner)
	default:
		return nil
	}
}

// capabilityOf reads Capabilities() through the optional interface the
// adapters implement (the method is not part of ModelProvider).
func capabilityOf(p ModelProvider) Capabilities {
	if c, ok := p.(interface{ Capabilities() Capabilities }); ok {
		return c.Capabilities()
	}
	return Capabilities{}
}

func runScopeSpec(typ, baseURL, model string) Spec {
	return Spec{
		ID:      "p",
		Type:    typ,
		BaseURL: baseURL,
		Model:   model,
		APIKey:  "sk-test",
		Headers: map[string]string{"X-Trace": "runscope"},
	}
}

func runScopedAdapters() []runScopeCase {
	return []runScopeCase{
		{
			name:         "openai-compatible",
			hasReasoning: true,
			hasClient:    true,
			mk: func(t *testing.T) ModelProvider {
				return NewOpenAICompatible(runScopeSpec("openai-compatible", "https://api.example.com/v1", "test-model"))
			},
		},
		{
			name:         "anthropic",
			hasReasoning: true,
			hasClient:    true,
			mk: func(t *testing.T) ModelProvider {
				return NewAnthropicProvider(runScopeSpec("anthropic", "https://api.anthropic.com", "claude-sonnet-4-5"))
			},
		},
		{
			name:         "gemini",
			hasReasoning: true,
			hasClient:    true,
			mk: func(t *testing.T) ModelProvider {
				return NewGeminiProvider(runScopeSpec("gemini", "https://generativelanguage.googleapis.com", "gemini-2.5-flash"))
			},
		},
		{
			name:         "openai-responses",
			hasReasoning: true,
			hasClient:    true,
			mk: func(t *testing.T) ModelProvider {
				return NewOpenAIResponses(runScopeSpec("openai-responses", "https://opencode.ai/zen/v1", "gpt-5.5"))
			},
		},
		{
			name:         "ollama",
			hasReasoning: true,
			hasClient:    true,
			mk: func(t *testing.T) ModelProvider {
				return NewOllamaProvider("http://127.0.0.1:11434", "ornith:9b")
			},
		},
		{
			name:         "vertex",
			hasReasoning: true,
			hasClient:    true,
			mk: func(t *testing.T) ModelProvider {
				v := NewVertexProvider(runScopeSpec("vertex", "https://us-central1-aiplatform.googleapis.com", "gemini-2.5-flash"))
				v.tokenFunc = func(ctx context.Context) (string, error) {
					return "vertex-token", nil
				}
				return v
			},
		},
		{
			name:         "bedrock",
			hasReasoning: false,
			hasClient:    false,
			mk: func(t *testing.T) ModelProvider {
				return NewBedrockProvider(Spec{ID: "p", Type: "bedrock", Region: "us-east-1", Model: "anthropic.claude-4"})
			},
		},
		{
			name:         "azure",
			// The Azure wrapper never exposed reasoning controls (they
			// ride the inner OpenAI-compatible spec); clones preserve that
			// exactly rather than inventing new behavior.
			hasReasoning: false,
			hasClient:    true,
			mk: func(t *testing.T) ModelProvider {
				spec := runScopeSpec("azure", "https://res.openai.azure.com", "gpt4o-deploy")
				spec.AuthMode = "bearer"
				spec.APIVersion = "2025-01-01-preview"
				return NewAzureProvider(spec)
			},
		},
		{
			name:         "opencode zen router",
			hasReasoning: true,
			hasClient:    true,
			mk: func(t *testing.T) ModelProvider {
				spec := runScopeSpec("opencode-zen", "https://opencode.ai/zen/v1", "glm-5.2")
				r, err := newOpenCodeRouter(spec, "OpenCode Zen", zenModels)
				if err != nil {
					t.Fatalf("newOpenCodeRouter: %v", err)
				}
				return r
			},
		},
	}
}

// TestRunScopedCloneContract pins the full RunScopedProvider contract per
// adapter, two clone levels deep:
//
//  1. clone is a distinct instance;
//  2. capabilities and configuration are preserved;
//  3. the HTTP client (and its connection pool) is shared where present;
//  4. request gates are independent (a clone gate never blocks the
//     original's slot);
//  5. reasoning state is independent.
func TestRunScopedCloneContract(t *testing.T) {
	for _, tc := range runScopedAdapters() {
		t.Run(tc.name, func(t *testing.T) {
			orig := tc.mk(t)
			level := []ModelProvider{orig}
			for depth := 1; depth <= 2; depth++ {
				parent := level[len(level)-1]
				clone, _, err := CloneForRun(parent)
				if err != nil {
					t.Fatalf("clone depth %d: %v", depth, err)
				}
				level = append(level, clone)

				if sameProviderInstance(orig, clone) || sameProviderInstance(parent, clone) {
					t.Fatal("CloneForRun returned the same instance")
				}

				if capabilityOf(clone) != capabilityOf(orig) {
					t.Errorf("depth %d capabilities changed: %+v -> %+v", depth, capabilityOf(orig), capabilityOf(clone))
				}
				if !reflect.DeepEqual(specOf(orig), specOf(clone)) {
					t.Errorf("depth %d configuration changed:\n%+v\nvs\n%+v", depth, specOf(orig), specOf(clone))
				}

				// Only the HTTP client may be shared; the gate must never be.
				if got, want := privateGate(orig), privateGate(clone); got == want || got == nil || want == nil {
					t.Fatalf("depth %d gates: original=%p clone=%p, want distinct non-nil gates", depth, got, want)
				}
				if tc.hasClient {
					if got, want := privateClient(orig), privateClient(clone); got == nil || got != want {
						t.Fatalf("depth %d client: original=%p clone=%p, want the same shared client", depth, got, want)
					}
				}
				// Gate independence is behavioral: holding the original's
				// slot must not occupy the clone's slot.
				if err := privateGate(orig).acquire(); err != nil {
					t.Fatalf("acquire original gate: %v", err)
				}
				if err := privateGate(clone).acquire(); err != nil {
					t.Fatalf("depth %d: clone gate blocked by the original's slot: %v", depth, err)
				}
				privateGate(orig).release()
				privateGate(clone).release()

				// Reasoning independence.
				if tc.hasReasoning {
					raOrig, ok := orig.(ReasoningAware)
					if !ok {
						t.Fatalf("adapter %T does not implement ReasoningAware", orig)
					}
					raClone, ok := clone.(ReasoningAware)
					if !ok {
						t.Fatalf("clone %T does not implement ReasoningAware", clone)
					}
					raOrig.SetReasoning(ReasoningConfig{Effort: "high"})
					if got := raClone.GetReasoning().Effort; got != "" {
						t.Errorf("depth %d: clone inherited reasoning %q, want zero state", depth, got)
					}
					raClone.SetReasoning(ReasoningConfig{Effort: "low"})
					if got := raOrig.GetReasoning().Effort; got != "high" {
						t.Errorf("depth %d: clone write leaked into the original: %q", depth, got)
					}
					if got := raClone.GetReasoning().Effort; got != "low" {
						t.Errorf("depth %d: clone reasoning write did not stick: %q", depth, got)
					}
				}
			}
		})
	}
}

// TestRunScopedVertexCloneAvoidsCopiedLockState pins the Vertex-specific
// rules: the clone never shares the mutex-guarded token-source state and
// re-resolves the ADC chain lazily per run, while the test seam
// (tokenFunc) and its semantics are preserved.
func TestRunScopedVertexCloneAvoidsCopiedLockState(t *testing.T) {
	origModel := ModelProvider(NewVertexProvider(runScopeSpec("vertex", "https://us-central1-aiplatform.googleapis.com", "gemini-2.5-flash")))
	orig := origModel.(*VertexProvider)
	calls := 0
	orig.tokenFunc = func(ctx context.Context) (string, error) {
		calls++
		return "vertex-token", nil
	}

	clone, _, err := CloneForRun(origModel)
	if err != nil {
		t.Fatalf("CloneForRun: %v", err)
	}
	c := clone.(*VertexProvider)

	if c == orig || c.ts != nil {
		t.Fatal("clone must start with independently resolved (empty) token state")
	}
	tok, err := c.token(context.Background())
	if err != nil {
		t.Fatalf("clone token: %v", err)
	}
	if tok != "vertex-token" || calls != 1 {
		t.Fatalf("token = %q calls = %d, want the shared test seam honored per run", tok, calls)
	}
	tokOrig, err := orig.token(context.Background())
	if err != nil || tokOrig != "vertex-token" || calls != 2 {
		t.Fatalf("original token = %q calls = %d err=%v, want the seam still owned by the original", tokOrig, calls, err)
	}
}

// TestRunScopedBedrockSeamSharedPerRun pins that the Bedrock SDK client
// seam is shared by reference: a clone invoking a request exercises the
// same lazy per-request construction the original carries (behavioral
// check; functional pointer comparison is impossible for func values).
func TestRunScopedBedrockSeamSharedPerRun(t *testing.T) {
	orig := ModelProvider(NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"}))
	var hits int
	orig.(*BedrockProvider).newClient = func(ctx context.Context, spec Spec) (bedrockConverseClient, error) {
		hits++
		return nil, fmt.Errorf("stub seam invoked")
	}
	clone, _, err := CloneForRun(orig)
	if err != nil {
		t.Fatalf("CloneForRun: %v", err)
	}

	if _, err := orig.StreamChat(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "stub seam") {
		t.Fatalf("original StreamChat err = %v, want the stubbed seam failure", err)
	}
	if _, err := clone.StreamChat(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "stub seam") {
		t.Fatalf("clone StreamChat err = %v, want the clone to reuse the shared seam", err)
	}
	if hits != 2 {
		t.Fatalf("seam invocations = %d, want one per provider request", hits)
	}
}

// TestRunScopedAzurePreservesDeploymentBehavior pins the Azure wrapper:
// the rebuilt clone keeps the deployment-scoped path, the pinned
// api-version, the api-key authentication header, and its own fresh gate.
func TestRunScopedAzurePreservesDeploymentBehavior(t *testing.T) {
	spec := Spec{
		ID: "az", Type: "azure", BaseURL: "https://res.openai.azure.com",
		Model: "gpt4o-deploy", APIVersion: "2025-01-01-preview",
	}
	orig := ModelProvider(NewAzureProvider(spec))
	clone, _, err := CloneForRun(orig)
	if err != nil {
		t.Fatalf("CloneForRun: %v", err)
	}
	c, ok := clone.(*AzureProvider)
	if !ok {
		t.Fatalf("clone type %T, want *AzureProvider", clone)
	}
	innerSpec := c.inner.spec
	if innerSpec.APIVersion != "2025-01-01-preview" {
		t.Errorf("cloned api-version = %q, want the configured value", innerSpec.APIVersion)
	}
	if innerSpec.ChatPath != "/openai/deployments/gpt4o-deploy/chat/completions" {
		t.Errorf("cloned chat path = %q, want the deployment-scoped path", innerSpec.ChatPath)
	}
	if innerSpec.AuthHeader != "api-key" || innerSpec.AuthMode != "header" {
		t.Errorf("cloned auth = %q/%q, want api-key header mode", innerSpec.AuthMode, innerSpec.AuthHeader)
	}
	if c.inner == orig.(*AzureProvider).inner {
		t.Error("clone shares the inner adapter; want a rebuilt instance")
	}}

// TestRunScopedOpenCodePreservesRoutedProtocol pins that router clones
// carry the resolved inner protocol adapter (a distinct instance of the
// same transport type) and forward reasoning to their own inner copy.
func TestRunScopedOpenCodePreservesRoutedProtocol(t *testing.T) {
	spec := runScopeSpec("opencode-zen", "https://opencode.ai/zen/v1", "glm-5.2")
	built, err := newOpenCodeRouter(spec, "OpenCode Zen", zenModels)
	if err != nil {
		t.Fatalf("newOpenCodeRouter: %v", err)
	}
	orig := ModelProvider(built)
	clone, _, err := CloneForRun(orig)
	if err != nil {
		t.Fatalf("CloneForRun: %v", err)
	}
	c, ok := clone.(*OpenCodeRouter)
	if !ok {
		t.Fatalf("clone type %T, want *OpenCodeRouter", clone)
	}
	o := orig.(*OpenCodeRouter)
	if c.protocol != o.protocol {
		t.Errorf("protocol = %q, want the resolved %q", c.protocol, o.protocol)
	}
	if reflect.TypeOf(c.inner) != reflect.TypeOf(o.inner) {
		t.Errorf("inner transport %T, want the same type as %T", c.inner, o.inner)
	}
	if sameProviderInstance(o.inner, c.inner) {
		t.Error("clone shares the inner adapter; want a distinct instance")
	}
	if got, want := privateGate(orig), privateGate(clone); got == want || got == nil || want == nil {
		t.Errorf("router gates: original=%p clone=%p, want the inner clone's own gate", got, want)
	}
	// Reasoning forwards into the router's own inner clone.
	rclone, ok := clone.(ReasoningAware)
	if !ok {
		t.Fatalf("router clone %T does not implement ReasoningAware", clone)
	}
	rclone.SetReasoning(ReasoningConfig{Effort: "high"})
	if got := clone.(ReasoningAware).GetReasoning().Effort; got != "high" {
		t.Errorf("router clone reasoning = %q, want high", got)
	}
	_, aware := c.inner.(ReasoningAware)
	if aware && c.inner.(ReasoningAware).GetReasoning().Effort != "high" {
		t.Errorf("inner clone reasoning = %q, want the forwarded setting", c.inner.(ReasoningAware).GetReasoning().Effort)
	}
	if o.inner.(ReasoningAware).GetReasoning().Effort != "" {
		t.Errorf("original inner reasoning = %q, want untouched zero state", o.inner.(ReasoningAware).GetReasoning().Effort)
	}
}

// TestCloneForRunFallbacks pins the helper's edge behavior: nil providers
// and non-built-in providers (test doubles, external providers) are not
// silently treated as cloneable production adapters.
func TestCloneForRunFallbacks(t *testing.T) {
	if _, _, err := CloneForRun(nil); err == nil {
		t.Fatal("CloneForRun(nil) succeeded, want an error")
	}

	fake := legacySharedProvider{}
	clone, cloneable, err := CloneForRun(fake)
	if err != nil {
		t.Fatalf("CloneForRun(fake): %v", err)
	}
	if cloneable {
		t.Error("cloneable = true for a non-built-in provider, want the legacy shared contract")
	}
	if clone != nil {
		t.Errorf("clone = %v, want nil for a non-cloneable provider", clone)
	}

	// A provider implementing the interface is honored regardless of type.
	willing := willingCloner{}
	got, cloneable, err := CloneForRun(willing)
	if err != nil || !cloneable {
		t.Fatalf("CloneForRun(willing) = (%v, %v, %v), want the interface honored", got, cloneable, err)
	}
	if got == nil {
		t.Error("clone = nil for a willing cloner")
	}
}

// legacySharedProvider is the pre-delegation contract made concrete: a
// provider with no run-scoped capability is shared between runs.
type legacySharedProvider struct{}

func (legacySharedProvider) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent)
	close(ch)
	return ch, nil
}

// willingCloner proves the helper honors interface implementations above
// all: an external provider that opts into run-scoped cloning is a
// supported configuration for test doubles and external adapters.
type willingCloner struct{}

func (willingCloner) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent)
	close(ch)
	return ch, nil
}

func (willingCloner) CloneForRun() (ModelProvider, error) {
	return &willingCloner{}, nil
}
