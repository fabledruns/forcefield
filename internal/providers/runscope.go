package providers

import (
	"fmt"
	"reflect"
)

// RunScopedProvider is the optional capability of built-in adapters that
// can hand out an independent provider instance for one concurrent
// runtime run (delegation spawns sibling child runs while the parent
// holds its own provider). Implementations guarantee:
//
//   - The returned provider belongs to exactly one runtime run. Its
//     mutable per-run state (the single-request gate, reasoning
//     selections) is never shared with the original or with any other
//     run's provider.
//   - Thread-safe objects may be shared: the *http.Client (and with it
//     the connection pool) and immutable configuration (Spec fields and
//     the configuration maps referenced by spec) are reused, never copied
//     per clone.
//   - Endpoint, model selection, authentication, protocol selection, and
//     observable behavior are preserved exactly.
//   - A clone that cannot be produced correctly fails with an error;
//     implementations never fall back to returning a shared mutable
//     instance.
//
// Providers that do not implement this capability keep the sequential-run
// contract from before delegation: the runtime shares them between runs
// (documented legacy contract for external and test providers).
type RunScopedProvider interface {
	CloneForRun() (ModelProvider, error)
}

// sameProviderInstance reports whether two providers are the same
// underlying instance. Reflection instead of == : external providers may
// hold non-comparable state, and comparing interfaces containing it would
// panic.
func sameProviderInstance(a, b ModelProvider) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Kind() != reflect.Pointer || vb.Kind() != reflect.Pointer {
		return false
	}
	return va.Pointer() == vb.Pointer()
}

// Compile-time guarantee: every built-in adapter supports run-scoped
// cloning. Adding a production adapter without a CloneForRun
// implementation fails the build here instead of failing delegation at
// runtime.
var (
	_ RunScopedProvider = (*OpenAICompatible)(nil) // also covers the NvidiaProvider alias
	_ RunScopedProvider = (*AnthropicProvider)(nil)
	_ RunScopedProvider = (*GeminiProvider)(nil)
	_ RunScopedProvider = (*VertexProvider)(nil)
	_ RunScopedProvider = (*BedrockProvider)(nil)
	_ RunScopedProvider = (*OllamaProvider)(nil)
	_ RunScopedProvider = (*OpenAIResponses)(nil)
	_ RunScopedProvider = (*AzureProvider)(nil)
	_ RunScopedProvider = (*OpenCodeRouter)(nil)
)

// isBuiltInAdapter reports whether p is a built-in production adapter.
// Built-ins must implement RunScopedProvider; CloneForRun turns a missing
// implementation into an error instead of a silent shared instance.
func isBuiltInAdapter(p ModelProvider) bool {
	switch p.(type) {
	case *OpenAICompatible, // also covers the NvidiaProvider alias
		*AzureProvider,
		*OpenCodeRouter,
		*AnthropicProvider,
		*GeminiProvider,
		*VertexProvider,
		*BedrockProvider,
		*OllamaProvider,
		*OpenAIResponses:
		return true
	default:
		return false
	}
}

// CloneForRun returns a run-scoped clone of a built-in provider. The
// cloneable result reports whether run-scoped cloning applies at all:
// false means p is not a built-in adapter (an external or test provider),
// and the caller decides how to run it (the runtime shares such providers
// under the legacy contract). Errors are definitive: a built-in adapter
// that cannot produce a valid clone must not be shared between runs.
func CloneForRun(p ModelProvider) (clone ModelProvider, cloneable bool, err error) {
	if p == nil {
		return nil, false, fmt.Errorf("providers: cannot clone a nil provider")
	}
	if c, ok := p.(RunScopedProvider); ok {
		got, err := c.CloneForRun()
		if err != nil {
			return nil, true, fmt.Errorf("providers: clone %T: %w", p, err)
		}
		if got == nil || sameProviderInstance(p, got) {
			return nil, true, fmt.Errorf("providers: clone %T returned an unusable clone", p)
		}
		return got, true, nil
	}
	if isBuiltInAdapter(p) {
		return nil, true, fmt.Errorf("providers: built-in %T does not implement run-scoped cloning", p)
	}
	return nil, false, nil
}

// CloneForRun gives the generic Chat Completions transport (and its
// NvidiaProvider alias) a run-scoped identity. The spec and retry policy
// are values; the HTTP client is shared and thread-safe; the request gate
// and reasoning state become per-run.
func (o *OpenAICompatible) CloneForRun() (ModelProvider, error) {
	if o == nil {
		return nil, fmt.Errorf("cannot clone a nil OpenAICompatible")
	}
	c := *o
	c.gate = newRequestGate()
	c.reasoning = ReasoningConfig{}
	return &c, nil
}

// CloneForRun clones the Anthropic Messages transport per run.
func (a *AnthropicProvider) CloneForRun() (ModelProvider, error) {
	if a == nil {
		return nil, fmt.Errorf("cannot clone a nil AnthropicProvider")
	}
	c := *a
	c.gate = newRequestGate()
	c.reasoning = ReasoningConfig{}
	return &c, nil
}

// CloneForRun clones the Gemini developer-API transport per run.
func (g *GeminiProvider) CloneForRun() (ModelProvider, error) {
	if g == nil {
		return nil, fmt.Errorf("cannot clone a nil GeminiProvider")
	}
	c := *g
	c.gate = newRequestGate()
	c.reasoning = ReasoningConfig{}
	return &c, nil
}

// CloneForRun clones the OpenAI Responses transport per run.
func (o *OpenAIResponses) CloneForRun() (ModelProvider, error) {
	if o == nil {
		return nil, fmt.Errorf("cannot clone a nil OpenAIResponses")
	}
	c := *o
	c.gate = newRequestGate()
	c.reasoning = ReasoningConfig{}
	return &c, nil
}

// CloneForRun clones the Ollama transport per run. Endpoint and Model are
// construction-time values and never written after construction, so a
// value copy carries them safely.
func (o *OllamaProvider) CloneForRun() (ModelProvider, error) {
	if o == nil {
		return nil, fmt.Errorf("cannot clone a nil OllamaProvider")
	}
	c := *o
	c.gate = newRequestGate()
	c.reasoning = ReasoningConfig{}
	return &c, nil
}

// CloneForRun builds a fresh Vertex provider per run. VertexProvider
// embeds a mutex-guarded lazy TokenSource, so it must never be copied by
// value: the clone starts with no cached token source and resolves the
// same ADC chain lazily on first use (short-lived tokens cached by
// oauth2 as before). The test seam (tokenFunc) is preserved so tests can
// clone Vertex providers without touching real credentials.
func (v *VertexProvider) CloneForRun() (ModelProvider, error) {
	if v == nil {
		return nil, fmt.Errorf("cannot clone a nil VertexProvider")
	}
	return &VertexProvider{
		spec:      v.spec,
		client:    v.client,
		retry:     v.retry,
		gate:      newRequestGate(),
		tokenFunc: v.tokenFunc,
	}, nil
}

// CloneForRun clones the Bedrock transport per run. The SDK client seam
// builds a fresh SDK client per request, so sharing the seam preserves
// behavior exactly; only the gate becomes per-run (Bedrock carries no
// reasoning state).
func (b *BedrockProvider) CloneForRun() (ModelProvider, error) {
	if b == nil {
		return nil, fmt.Errorf("cannot clone a nil BedrockProvider")
	}
	c := *b
	c.gate = newRequestGate()
	return &c, nil
}

// CloneForRun clones the Azure wrapper per run by cloning the inner
// OpenAI-compatible transport (fresh gate, fresh reasoning state) and
// reattaching it to the same immutable spec. The Azure-specific reshaping
// (deployment-scoped chat path, pinned api-version, api-key header,
// status hints) lives in the inner spec values and the hintFor closure,
// both of which the inner clone carries, so the wrapper keeps its exact
// configuration while the shared HTTP client and its connection pool
// survive the copy.
func (a *AzureProvider) CloneForRun() (ModelProvider, error) {
	if a == nil {
		return nil, fmt.Errorf("cannot clone a nil AzureProvider")
	}
	innerClone, err := a.inner.CloneForRun()
	if err != nil {
		return nil, err
	}
	return &AzureProvider{spec: a.spec, inner: innerClone.(*OpenAICompatible)}, nil
}

// CloneForRun clones the OpenCode router per run. The router itself is a
// plain struct; its inner wire-protocol adapter (the resolved transport -
// Chat Completions, Responses, or Anthropic Messages) is cloned so the
// routed protocol selection and its per-run state survive the copy.
func (r *OpenCodeRouter) CloneForRun() (ModelProvider, error) {
	if r == nil {
		return nil, fmt.Errorf("cannot clone a nil OpenCodeRouter")
	}
	inner, ok := r.inner.(RunScopedProvider)
	if !ok {
		return nil, fmt.Errorf("%s: inner transport %T does not support run-scoped cloning", r.service, r.inner)
	}
	innerClone, err := inner.CloneForRun()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.service, err)
	}
	c := *r
	c.inner = innerClone
	c.reasoning = ReasoningConfig{}
	return &c, nil
}
