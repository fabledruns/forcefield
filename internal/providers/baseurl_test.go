package providers

import (
	"strings"
	"testing"
)

// TestSpecValidationRejectsBaseURLQueryAndFragment pins the base-URL
// shape rule: providers entries are API roots, and every adapter builds
// request URLs by plain concatenation. A query or fragment in base_url
// would corrupt paths or silently drop appended parameters (Azure's
// api-version, Vertex query flags), so any "?" or "#" - including bare
// delimiters the URL parser drops - must fail validation naming the
// provider and the offending value.
func TestSpecValidationRejectsBaseURLQueryAndFragment(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		typ     string
	}{
		{"query", "https://host/v1?env=prod", "openai-compatible"},
		{"fragment", "https://host/v1#default", "openai-compatible"},
		{"query and fragment", "https://host/v1?a=1#f", "openai-compatible"},
		{"bare question mark", "https://host/v1?", "openai-compatible"},
		{"bare hash", "https://host/v1#", "openai-compatible"},
		{"query after path-free root", "https://host?", "openai-compatible"},
		{"azure api-version in query", "https://res.openai.azure.com/?api-version=2024-06-01", "openai-compatible"},
		{"anthropic query", "https://api.anthropic.com?beta=true", "anthropic"},
		{"gemini fragment", "https://generativelanguage.googleapis.com#v1", "gemini"},
		{"responses transport query", "https://gateway.example/v1?x=1", "openai-responses"},
		{"ollama query", "http://localhost:11434?parallel=2", "ollama"},
		{"vertex base query", "https://us-central1-aiplatform.googleapis.com?x=1", "vertex"},
		{"bedrock override query", "https://vpce.us-east-1.vpce.amazonaws.com?x=1", "bedrock"},
		{"bedrock override fragment", "https://vpce.us-east-1.vpce.amazonaws.com#x", "bedrock"},
		{"bedrock bare question mark", "https://vpce.us-east-1.vpce.amazonaws.com?", "bedrock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := Spec{ID: "p", Type: tc.typ, BaseURL: tc.baseURL}
			err := spec.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted base_url %q", tc.baseURL)
			}
			if !strings.Contains(err.Error(), "query or fragment") {
				t.Errorf("error = %q, want a query-or-fragment rejection", err)
			}
			if !strings.Contains(err.Error(), tc.baseURL) {
				t.Errorf("error = %q, want it to name the offending value", err)
			}
		})
	}
}

// TestSpecValidationAllowsPlainBaseURLs guards the other side: legitimate
// endpoint shapes - paths, ports, trailing slashes, IPv6 literal hosts -
// must keep validating, so the rule rejects only suffixes.
func TestSpecValidationAllowsPlainBaseURLs(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		typ     string
	}{
		{"plain host", "https://api.example.com", "openai-compatible"},
		{"path", "https://api.example.com/v1", "openai-compatible"},
		{"path and port", "http://localhost:1234/v1", "openai-compatible"},
		{"trailing slash", "https://api.example.com/v1/", "openai-compatible"},
		{"deep path", "https://host/api/v2/openai", "openai-compatible"},
		{"ipv6 literal", "http://[::1]:11434", "ollama"},
		{"ipv6 loopback with path", "http://[::1]:8080/v1", "openai-compatible"},
		{"anthropic root", "https://api.anthropic.com", "anthropic"},
		{"gemini root", "https://generativelanguage.googleapis.com", "gemini"},
		{"vertex regional", "https://us-central1-aiplatform.googleapis.com", "vertex"},
		{"azure resource", "https://res.openai.azure.com", "openai-compatible"},
		{"percent escape preserved", "https://host/a%20b", "openai-compatible"},
		{"userinfo host", "https://res.cloud:8443/v1", "openai-compatible"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := Spec{ID: "p", Type: tc.typ, BaseURL: tc.baseURL}
			if err := spec.Validate(); err != nil {
				t.Errorf("Validate() rejected plain base_url %q: %v", tc.baseURL, err)
			}
		})
	}
}

// TestBedrockEndpointOverrideRejectsSuffixes covers the SDK-seam
// validation that backs bedrock's endpoint override directly, so the
// shared rule cannot drift out of the two layers.
func TestBedrockEndpointOverrideRejectsSuffixes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"query", "https://endpoint.example?x=1"},
		{"fragment", "https://endpoint.example#f"},
		{"bare question mark", "https://endpoint.example?"},
		{"bare hash", "https://endpoint.example#"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := urlParseRequestURI(tc.raw); err == nil {
				t.Fatalf("urlParseRequestURI(%q) succeeded, want rejection", tc.raw)
			} else if !strings.Contains(err.Error(), "query or fragment") {
				t.Errorf("error = %q, want a query-or-fragment rejection", err)
			}
		})
	}
	if _, err := urlParseRequestURI("https://endpoint.example:443/path/"); err != nil {
		t.Errorf("urlParseRequestURI rejected a plain override: %v", err)
	}
}
