package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

// azureServer builds an Azure provider against a test server. The
// handler sees every request; SSE lines it writes are flushed verbatim.
func azureServer(t *testing.T, spec Spec, handle func(t *testing.T, w http.ResponseWriter, r *http.Request)) *AzureProvider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(t, w, r)
	}))
	t.Cleanup(server.Close)
	spec.ID = "azure-test"
	spec.Type = "azure"
	spec.Label = "Azure Test"
	spec.BaseURL = server.URL
	return NewAzureProvider(spec)
}

func TestAzureProviderDeploymentURLAndAPIKeyHeader(t *testing.T) {
	var gotPath, gotQuery, gotAPIKey, gotAuth string
	var gotBody map[string]any
	p := azureServer(t, Spec{Model: "gpt4o-deploy", APIKey: "azure-key-123"}, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAPIKey = r.Header.Get("api-key")
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Azure's first stream chunk carries prompt_filter_results
		// with an empty choices array; the shared parser skips it.
		fmt.Fprint(w, `data: {"object":"chat.completion.chunk","choices":[],"prompt_filter_results":[{"content_filter_result":{"filtered":false,"severity":"safe"}}]}`+"\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	stream, err := p.StreamChat(context.Background(),
		[]Message{{Role: SystemRole, Content: "be brief"}, {Role: UserRole, Content: "hi"}},
		[]tools.Definition{{Name: "echo", Description: "Echo.", InputSchema: map[string]any{"type": "object"}}},
	)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	events := drainEvents(t, stream)

	if gotPath != "/openai/deployments/gpt4o-deploy/chat/completions" {
		t.Errorf("path = %q, want the deployment-scoped chat path", gotPath)
	}
	if gotQuery != "api-version=2024-06-01" {
		t.Errorf("query = %q, want the default api-version", gotQuery)
	}
	if gotAPIKey != "azure-key-123" {
		t.Errorf("api-key header = %q, want the configured key", gotAPIKey)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (Azure key auth uses api-key)", gotAuth)
	}
	if gotBody["model"] != "gpt4o-deploy" || gotBody["stream"] != true {
		t.Errorf("model/stream = %v/%v, want gpt4o-deploy/true", gotBody["model"], gotBody["stream"])
	}
	if _, ok := gotBody["tools"].([]any); !ok {
		t.Errorf("tools = %#v, want one function definition", gotBody["tools"])
	}

	var text string
	for _, e := range events {
		text += e.Text
		if e.Err != nil {
			t.Fatalf("stream error event = %v", e.Err)
		}
	}
	if text != "ok" {
		t.Errorf("text = %q, want ok", text)
	}
}

func TestAzureProviderCustomAPIVersion(t *testing.T) {
	var gotQuery string
	p := azureServer(t, Spec{Model: "m", APIKey: "k", APIVersion: "2025-01-01-preview"}, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	if gotQuery != "api-version=2025-01-01-preview" {
		t.Errorf("query = %q, want the configured api-version", gotQuery)
	}
}

func TestAzureProviderEntraBearerMode(t *testing.T) {
	var gotAPIKey, gotAuth string
	p := azureServer(t, Spec{Model: "m", APIKey: "entra-token-abc", AuthMode: "bearer"}, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("api-key")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	if gotAuth != "Bearer entra-token-abc" {
		t.Errorf("Authorization = %q, want the Entra token as a bearer credential", gotAuth)
	}
	if gotAPIKey != "" {
		t.Errorf("api-key header = %q, want empty in Entra mode", gotAPIKey)
	}
}

func TestAzureProviderDeploymentNotFoundHint(t *testing.T) {
	p := azureServer(t, Spec{Model: "gpt4o-deploy", APIKey: "k"}, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":"404","message":"The API deployment 'gpt4o-deploy' does not exist in this region."}}`)
	})
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() error = nil, want a 404 error")
	}
	if !strings.Contains(err.Error(), "deployment") || !strings.Contains(err.Error(), "gpt4o-deploy") {
		t.Errorf("error = %q, want it to name the deployment", err)
	}
	if !strings.Contains(err.Error(), "deployment name") {
		t.Errorf("error = %q, want the deployment-name semantics explained", err)
	}
}

func TestAzureProviderContentFilterHint(t *testing.T) {
	p := azureServer(t, Spec{Model: "m", APIKey: "k"}, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":"content_policy_violation","message":"The response was filtered due to the prompt triggering Azure OpenAI's content management policy.","innererror":{"code":"ResponsibleAIPolicyViolation","content_filter_result":{"filtered":true}}}}`)
	})
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() error = nil, want a 400 content-filter error")
	}
	if !strings.Contains(err.Error(), "content filter") {
		t.Errorf("error = %q, want the content filter named", err)
	}
}

func TestAzureProviderAPIVersionHint(t *testing.T) {
	p := azureServer(t, Spec{Model: "m", APIKey: "k"}, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":"InvalidParameterError","message":"Unsupported api-version, valid: 2025-04-01-preview."}}`)
	})
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() error = nil, want a 400 api-version error")
	}
	if !strings.Contains(err.Error(), "api_version") {
		t.Errorf("error = %q, want it to name the api_version config key", err)
	}
}

func TestAzureProviderUnauthorizedHintForEntra(t *testing.T) {
	p := azureServer(t, Spec{Model: "m", APIKey: "expired", AuthMode: "bearer"}, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"code":"401","message":"Access denied due to invalid subscription key or wrong API endpoint"}}`)
	})
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() error = nil, want a 401 error")
	}
	if !strings.Contains(err.Error(), "Entra") || !strings.Contains(err.Error(), "cognitiveservices.azure.com") {
		t.Errorf("error = %q, want Entra scope guidance", err)
	}
}

func TestAzureProviderRequiresModel(t *testing.T) {
	p := NewAzureProvider(Spec{ID: "azure-test", Type: "azure", BaseURL: "https://myres.openai.azure.com"})
	// Construction must tolerate an empty model (the capability probe
	// builds every transport without one); requests fail closed.
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() error = nil, want an empty-model error")
	}
	if !strings.Contains(err.Error(), "deployment") {
		t.Errorf("error = %q, want the deployment-name semantics explained", err)
	}
}

func TestAzureProviderRejectsTraversalDeployment(t *testing.T) {
	// PathEscape leaves "." and ".." verbatim; a ".." deployment would
	// retarget the path on the resource host. Reject it fail-closed.
	for _, model := range []string{"..", "../..", "a/../b"} {
		p := NewAzureProvider(Spec{ID: "azure-test", Type: "azure", BaseURL: "https://myres.openai.azure.com", Model: model})
		_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
		if err == nil {
			t.Fatalf("StreamChat() with deployment %q: error = nil, want rejection", model)
		}
		if !strings.Contains(err.Error(), "..") {
			t.Errorf("error = %q, want it to name the traversal segment", err)
		}
	}
}

func TestAzureProviderCapabilities(t *testing.T) {
	p := NewAzureProvider(Spec{
		ID:      "azure-test",
		Type:    "azure",
		BaseURL: "https://myres.openai.azure.com",
		Model:   "m",
	})
	caps := p.Capabilities()
	if !caps.Streaming || !caps.ToolCalling {
		t.Errorf("capabilities = %+v, want streaming and tool calling", caps)
	}
	if caps.Reasoning {
		t.Error("capabilities.Reasoning = true, want false for user-named deployments")
	}

	override := NewAzureProvider(Spec{
		ID:      "azure-test",
		Type:    "azure",
		BaseURL: "https://myres.openai.azure.com",
		Model:   "m",
		CapabilitiesOverride: &CapabilitiesOverride{
			Reasoning: boolPtr(true),
		},
	})
	if !override.Capabilities().Reasoning {
		t.Error("capabilities.reasoning override did not take effect")
	}
}

func TestAzureFactoryRegistered(t *testing.T) {
	if !DefaultFactories().HasType("azure") {
		t.Fatal("azure is not registered in the default factory registry")
	}
	p, err := DefaultFactories().Create(Spec{
		ID:      "azure-test",
		Type:    "azure",
		BaseURL: "https://myres.openai.azure.com",
		Model:   "gpt4o",
	})
	if err != nil {
		t.Fatalf("Create(azure) error = %v", err)
	}
	if _, ok := p.(*AzureProvider); !ok {
		t.Errorf("provider = %T, want *AzureProvider", p)
	}
}

func TestSpecValidateAPIVersion(t *testing.T) {
	base := Spec{ID: "p", Type: "azure", BaseURL: "https://myres.openai.azure.com", Model: "m"}
	ok := base
	ok.APIVersion = "2024-06-01"
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want a valid api-version accepted", err)
	}
	injection := base
	injection.APIVersion = "2024-06-01&max_tokens=1"
	if err := injection.Validate(); err == nil {
		t.Error("Validate() accepted an api-version with query-injection characters")
	}
	spaced := base
	spaced.APIVersion = "2024 06"
	if err := spaced.Validate(); err == nil {
		t.Error("Validate() accepted an api-version with a space")
	}
}
