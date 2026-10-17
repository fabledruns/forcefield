package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"forcefield/internal/tools"
)

// AzureProvider serves Azure OpenAI / AI Foundry resources through the
// classic deployment-scoped Chat Completions API:
//
//	POST {resource}/openai/deployments/{deployment}/chat/completions?api-version={ver}
//
// The wire protocol is OpenAI Chat Completions; Azure differs in three
// ways this adapter owns: the deployment name lives in the URL path (the
// configured model ID *is* the deployment name), every data-plane call
// pins an api-version query parameter, and key authentication uses the
// "api-key" header rather than Authorization. The shared OpenAICompatible
// transport owns payloads, SSE streaming, tool-call reassembly, retries,
// and the request gate; this adapter only reshapes the spec and adds
// Azure-specific error guidance. See docs/Providers.md.
type AzureProvider struct {
	spec  Spec
	inner *OpenAICompatible
}

// defaultAzureAPIVersion pins the GA api-version used when config does
// not choose one. It is the documented GA data-plane release for Chat
// Completions (Microsoft Learn: Azure OpenAI REST API reference);
// api_version overrides it for resources that need another release.
const defaultAzureAPIVersion = "2024-06-01"

// NewAzureProvider builds an Azure resource adapter from a resolved
// spec. Model is the deployment name; because deployments are unique per
// resource, no model IDs are cataloged and none are inferred. An empty
// model is tolerated at construction (the capability probe builds every
// transport without one); StreamChat refuses to send a request without a
// deployment.
func NewAzureProvider(spec Spec) *AzureProvider {
	deployment := strings.TrimSpace(spec.Model)

	innerSpec := spec
	innerSpec.Model = deployment
	if deployment != "" {
		innerSpec.ChatPath = "/openai/deployments/" + url.PathEscape(deployment) + "/chat/completions"
	}
	innerSpec.APIVersion = spec.APIVersion
	if innerSpec.APIVersion == "" {
		innerSpec.APIVersion = defaultAzureAPIVersion
	}
	// Azure key auth travels in the fixed "api-key" header. Bearer mode
	// is kept verbatim for Microsoft Entra ID tokens, which live in the
	// Authorization header exactly as the generic transport sends them;
	// "none" is preserved for proxies that terminate auth elsewhere.
	switch spec.NormalizedAuthMode() {
	case "", "header":
		innerSpec.AuthMode = "header"
		innerSpec.AuthHeader = "api-key"
		innerSpec.AuthScheme = ""
	}

	inner := NewOpenAICompatible(innerSpec)
	inner.hintFor = azureStatusHint(spec, innerSpec.APIVersion)
	return &AzureProvider{spec: spec, inner: inner}
}

// StreamChat delegates to the shared OpenAI transport; only the URL and
// auth header differ, and both are baked into the inner spec. The
// deployment-name checks fail closed here rather than at construction so
// the capability probe (empty model) can still build the adapter.
func (a *AzureProvider) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	deployment := strings.TrimSpace(a.spec.Model)
	if deployment == "" {
		return nil, fmt.Errorf("no model configured - on Azure the model name is the deployment name (Azure portal > Deployments); set model.name or providers.%s.model", a.spec.ID)
	}
	// PathEscape leaves "." and ".." verbatim; a traversal segment can
	// only ever retarget a path on the same resource host, but reject it
	// anyway so the deployment path stays exactly what it claims to be
	// (Azure's own deployment naming rules disallow such names).
	for _, seg := range strings.Split(deployment, "/") {
		if seg == ".." {
			return nil, fmt.Errorf("provider %q model %q is not a valid Azure deployment name - deployment names may not contain \"..\" path segments", a.spec.ID, deployment)
		}
	}
	return a.inner.StreamChat(ctx, messages, defs)
}

// Capabilities reports what this transport supports. Reasoning stays
// false by default: deployments are user-named, so no per-model
// reasoning contract can be inferred, and sending reasoning_effort to a
// model that rejects it fails the whole request. capabilities.reasoning
// in config.yaml opts in explicitly.
func (a *AzureProvider) Capabilities() Capabilities {
	base := Capabilities{
		Streaming:       true,
		ToolCalling:     true,
		MaxOutputTokens: 4096,
	}
	return a.spec.CapabilitiesOverride.Apply(base)
}

// azureStatusHint turns Azure-specific failure modes into concrete
// guidance: the deployment-name semantics, the two auth paths (api-key
// header vs Entra token), content filtering, and api-version mismatches.
func azureStatusHint(spec Spec, apiVersion string) func(int, string) string {
	return func(status int, body string) string {
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden:
			if spec.NormalizedAuthMode() == "bearer" {
				return "Microsoft Entra ID token rejected - the token must target scope https://cognitiveservices.azure.com/.default and the identity needs the Cognitive Services OpenAI User role on this resource; refresh it with `az account get-access-token --resource https://cognitiveservices.azure.com`"
			}
			if spec.APIKey == "" {
				return "no API key is configured - set AZURE_OPENAI_API_KEY (or api_key_env in config.yaml); Azure authenticates keys via the api-key header, or use auth_mode: bearer with an Entra token"
			}
			return "check that the API key is valid for this Azure resource and that key authentication is enabled (Entra-only resources reject api-key)"
		case http.StatusNotFound:
			return fmt.Sprintf("deployment %q was not found in this Azure resource - the model name IS the deployment name (Azure portal > Deployments); pick an existing deployment or create one", spec.Model)
		case http.StatusBadRequest:
			if strings.Contains(body, "content_filter") || strings.Contains(body, "ResponsibleAIPolicyViolation") {
				return "Azure's content filter blocked the request - rephrase the prompt or adjust the resource's content filter policy (Azure AI Foundry > Content filters)"
			}
			if strings.Contains(body, "api-version") || strings.Contains(body, "api_version") {
				return fmt.Sprintf("this resource rejected api-version %s - set providers.%s.api_version to an api-version your resource supports (Microsoft Learn: Azure OpenAI REST API reference)", apiVersion, spec.ID)
			}
			return ""
		case http.StatusTooManyRequests:
			return "Azure rate limit or deployment quota (TPM) reached - check the deployment's quota in the Azure portal; Forcefield retries briefly, then gives up"
		default:
			return ""
		}
	}
}
