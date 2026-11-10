package cue

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"forcefield/internal/config"
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

// TestSchemaOrchestrationCrossField pins the orchestration cross-field
// parity (fix O2): Go's validateOrchestration rejects an explicit
// concurrency cap below an explicit depth (a nested chain holds one child
// slot per depth level at the same time, so the capped chain stalls on
// the semaphore until the delegate timeout), and config.cue mirrors that
// rule behind a presence guard so `cue vet` rejects the same
// configurations. The guard follows the real rule exactly: the relation
// only exists when BOTH fields appear - omitted values resolve to the
// documented defaults (depth <= 3, concurrency 4) which always satisfy
// it.
//
// The fixture files are shared with the CI cue job (.github/workflows/
// test.yml), which enforces the CUE verdict on them; the checks below
// enforce the corresponding verdicts through the real Go Load path, so
// both engines are demonstrated against the same configurations.
func TestSchemaOrchestrationCrossField(t *testing.T) {
	raw, err := os.ReadFile(configCue)
	if err != nil {
		t.Fatalf("read %s: %v", configCue, err)
	}
	schema := string(raw)

	// The guard and the relation must both be present, conditioned on
	// both fields appearing.
	for _, want := range []string{
		"if max_depth != _|_ && max_concurrent_children != _|_ {",
		"max_concurrent_children: >= max_depth",
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("config.cue lost the guarded cross-field constraint, want literal %q", want)
		}
	}

	// Parity must stay exact in BOTH directions: Go intentionally does
	// not relate max_children_per_run to max_depth (a small spawn cap
	// only makes deep chains unreachable, and Go refuses every excess
	// spawn honestly), so a constraint to that effect in the schema
	// would reject configurations Go accepts. This literal keeps the
	// phantom rule out of the schema.
	if strings.Contains(schema, "max_children_per_run: >= max_depth") {
		t.Fatalf("config.cue bounds max_children_per_run against max_depth; sys validation has no such rule, so the schema would reject configs Go accepts")
	}

	// loadGoConfig runs the real Go load path against one YAML body in
	// an isolated home directory (mirrors internal/config's own test
	// harness via the exported Load API).
	loadGoConfig := func(t *testing.T, body string) error {
		t.Helper()
		home := t.TempDir()
		t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
		t.Setenv("HOME", home)        // os.UserHomeDir everywhere else
		path, err := config.Path()
		if err != nil {
			t.Fatalf("config.Path: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create home dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err = config.Load()
		return err
	}

	// The CI-shared fixtures must carry the same verdicts here that the
	// cue job enforces there.
	invalid, err := os.ReadFile("testdata/invalid-orchestration-crossfield.yaml")
	if err != nil {
		t.Fatalf("read cross-field fixture: %v", err)
	}
	err = loadGoConfig(t, string(invalid))
	if err == nil {
		t.Fatalf("Go Load accepted the deadlocking fixture CUE rejects (max_depth: 3, max_concurrent_children: 2)")
	}
	for _, want := range []string{"max_concurrent_children", "max_depth"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Go Load error %q does not name %q", err, want)
		}
	}
	for _, ok := range []string{
		"testdata/valid-orchestration-crossfield.yaml",
		"testdata/valid-orchestration-depth-only.yaml",
	} {
		body, ferr := os.ReadFile(ok)
		if ferr != nil {
			t.Fatalf("read %s: %v", ok, ferr)
		}
		if err := loadGoConfig(t, string(body)); err != nil {
			t.Fatalf("Go Load rejected %s: %v", ok, err)
		}
	}

	// The exact boundary: the load path accepts cap == depth and
	// the single-field forms, and rejects cap < depth with an error
	// naming both fields.
	modelBlock := `model:
  provider: ollama
  endpoint: http://localhost:11434
  name: ornith:9b
agent:
  name: default
permissions:
  default: ask
orchestration:
`
	for _, tc := range []struct {
		name   string
		bounds string
		reject bool
	}{
		{"depth 3 cap 3 (boundary)", "  enabled: true\n  max_depth: 3\n  max_concurrent_children: 3", false},
		{"depth 2 cap 2 (boundary)", "  enabled: true\n  max_depth: 2\n  max_concurrent_children: 2", false},
		{"depth 1 cap 1 (floor)", "  enabled: true\n  max_depth: 1\n  max_concurrent_children: 1", false},
		{"depth only (default cap)", "  enabled: true\n  max_depth: 3", false},
		{"cap only", "  enabled: true\n  max_concurrent_children: 1", false},
		{"depth 3, per_run 1 (no relation)", "  enabled: true\n  max_depth: 3\n  max_children_per_run: 1", false},
		{"depth 3 cap 2 (deadlock)", "  enabled: true\n  max_depth: 3\n  max_concurrent_children: 2", true},
		{"depth 2 cap 1 (deadlock)", "  enabled: true\n  max_depth: 2\n  max_concurrent_children: 1", true},
	} {
		err := loadGoConfig(t, modelBlock+tc.bounds)
		if tc.reject && err == nil {
			t.Errorf("%s: Go Load accepted, want rejection", tc.name)
		}
		if !tc.reject && err != nil {
			t.Errorf("%s: Go Load rejected: %v", tc.name, err)
		}
		if tc.reject && err != nil && !strings.Contains(err.Error(), "max_concurrent_children") {
			t.Errorf("%s: error %q does not name max_concurrent_children", tc.name, err)
		}
	}
}
