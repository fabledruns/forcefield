package cue

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The CUE schemas are the declarative mirror of the Go validation rules.
// Forcefield does not evaluate CUE at load time, so these tests pin the
// documented constraints by compiling them with Go's RE2 engine (the same
// syntax family CUE's =~ uses) and checking they exactly track the
// behavior the Go validators enforce.

const configCue = "config.cue"
const providersCue = "providers.cue"

// loadCUEConstraint extracts the regex body of one `name: string & =~ "..."`
// constraint from a CUE file.
func loadCUEConstraint(t *testing.T, path, name string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(name + `:\s*string & =~ "([^"]*)"`)
	m := re.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("constraint %s not found in %s", name, path)
	}
	return m[1]
}

// TestHTTPURLConstraintPinsQueryAndFragmentRejection verifies the CUE
// #httpURL pattern rejects the same URLs the Go validators reject:
// any literal "?" or "#" (query strings, fragments, and bare delimiters
// the URL parser would otherwise drop) while accepting ordinary plain
// endpoints - paths, ports, and trailing slashes included.
func TestHTTPURLConstraintPinsQueryAndFragmentRejection(t *testing.T) {
	pattern := loadCUEConstraint(t, configCue, "#httpURL")
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("#httpURL pattern %q does not compile: %v", pattern, err)
	}

	rejected := []string{
		"https://host/v1?x=1",
		"https://host/v1#f",
		"https://host/v1?x=1#f",
		"https://host/v1?",
		"https://host/v1#",
		"http://host?",
	}
	accepted := []string{
		"http://localhost:11434",
		"https://api.openai.com/v1",
		"https://host:8443/v1/",
		"https://us-central1-aiplatform.googleapis.com",
		"https://res.openai.azure.com",
		"https://opencode.ai/zen/go/v1",
	}
	for _, u := range rejected {
		if re.MatchString(u) {
			t.Errorf("pattern %q accepted %q, want rejection", pattern, u)
		}
	}
	for _, u := range accepted {
		if !re.MatchString(u) {
			t.Errorf("pattern %q rejected plain endpoint %q", pattern, u)
		}
	}
}

// TestProviderCatalogEndpointsMatchHTTPURL checks every preset endpoint in
// providers.cue against the shared #httpURL constraint: the catalog is
// the set of URLs config switching adopts verbatim, so its entries must
// stay inside the tightened shape rule.
func TestProviderCatalogEndpointsMatchHTTPURL(t *testing.T) {
	pattern := loadCUEConstraint(t, configCue, "#httpURL")
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("#httpURL pattern %q does not compile: %v", pattern, err)
	}

	raw, err := os.ReadFile(providersCue)
	if err != nil {
		t.Fatalf("read %s: %v", providersCue, err)
	}
	endpointRe := regexp.MustCompile(`endpoint:\s*"([^"]+)"`)
	matches := endpointRe.FindAllStringSubmatch(string(raw), -1)
	if len(matches) == 0 {
		t.Fatalf("no catalog endpoints found in %s", providersCue)
	}
	for _, m := range matches {
		endpoint := m[1]
		if !re.MatchString(endpoint) {
			t.Errorf("catalog endpoint %q does not satisfy #httpURL %q", endpoint, pattern)
		}
		if strings.ContainsAny(endpoint, "?#") {
			t.Errorf("catalog endpoint %q carries a query or fragment", endpoint)
		}
	}
}
