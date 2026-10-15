package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func isolateProviderHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// Provider add/test flags are package globals; reset them so tests do
	// not leak flag state into each other.
	providerAddName, providerAddType, providerAddBaseURL = "", "openai-compatible", ""
	providerAddModel, providerAddAPIKeyEnv = "", ""
	providerAddAuthMode, providerAddAuthHdr, providerAddAuthSchm = "", "", ""
	providerAddAPIPath, providerAddModels = "", nil
	providerAddTimeout, providerAddForce = 0, false
	providerAddProject, providerAddLocation, providerAddCredsFile = "", "", ""
	providerTestChat, providerTestTimeout = false, 0
	return home
}

func writeProviderConfig(t *testing.T, body string) {
	t.Helper()
	path, err := config.Path()
	if err != nil {
		t.Fatalf("config.Path() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestProviderAddAndRemove(t *testing.T) {
	isolateProviderHome(t)
	writeProviderConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\n")

	providerAddName = "my-local"
	providerAddBaseURL = "http://localhost:1234/v1"
	providerAddModel = "m1"
	providerAddModels = []string{"m1", "m2"}
	if err := providerAddCmd.RunE(providerAddCmd, nil); err != nil {
		t.Fatalf("add error = %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	entry, ok := cfg.Providers["my-local"]
	if !ok {
		t.Fatal("added provider missing from config")
	}
	if entry.BaseURL != "http://localhost:1234/v1" || entry.Model != "m1" || len(entry.Models) != 2 {
		t.Errorf("entry = %+v, want base/model/models preserved", entry)
	}

	// Duplicate without --force fails.
	providerAddName = "my-local"
	providerAddBaseURL = "http://localhost:1234/v1"
	providerAddForce = false
	if err := providerAddCmd.RunE(providerAddCmd, nil); err == nil {
		t.Fatal("duplicate add succeeded without --force")
	}

	// Remove non-active succeeds.
	if err := providerRemoveCmd.RunE(providerRemoveCmd, []string{"my-local"}); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	cfg, _ = config.Load()
	if _, ok := cfg.Providers["my-local"]; ok {
		t.Error("provider still present after remove")
	}
}

func TestProviderRemoveActiveRefused(t *testing.T) {
	isolateProviderHome(t)
	writeProviderConfig(t, "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: m\nproviders:\n  extra:\n    type: openai-compatible\n    base_url: http://localhost:9/v1\n")

	// Switch active to extra, then removal must refuse.
	cfg, _ := config.Load()
	cfg.Model.Provider = "extra"
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := providerRemoveCmd.RunE(providerRemoveCmd, []string{"extra"}); err == nil {
		t.Fatal("remove of active provider succeeded, want refusal")
	} else if !strings.Contains(err.Error(), "active") {
		t.Errorf("error = %q, want it to mention active", err)
	}
}

func TestProviderTestDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q, want bearer", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"m1"},{"id":"m2"}]}`)
	}))
	defer srv.Close()

	isolateProviderHome(t)
	t.Setenv("TEST_CUSTOM_KEY", "sk-test")
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m1\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: "+srv.URL+"\n    api_key_env: TEST_CUSTOM_KEY\n    models:\n      - m1\n")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := runProviderTest(cfg, "custom", false, 0); err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
}

func TestProviderTestHeaderAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Custom-Auth"); got != "hdr-key" {
			t.Errorf("X-Custom-Auth = %q, want hdr-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty in header mode", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	isolateProviderHome(t)
	t.Setenv("TEST_HDR_KEY", "hdr-key")
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: "+srv.URL+"\n    api_key_env: TEST_HDR_KEY\n    auth_mode: header\n    auth_header: X-Custom-Auth\n")

	cfg, _ := config.Load()
	if err := runProviderTest(cfg, "custom", false, 0); err != nil {
		t.Fatalf("runProviderTest() error = %v", err)
	}
}

func TestProviderTestNeverLeaksKey(t *testing.T) {
	const secret = "sk-never-print-this"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":"bad key %s"}`, secret)
	}))
	defer srv.Close()

	isolateProviderHome(t)
	t.Setenv("TEST_LEAK_KEY", secret)
	writeProviderConfig(t, "model:\n  provider: custom\n  name: m\nproviders:\n  custom:\n    type: openai-compatible\n    base_url: "+srv.URL+"\n    api_key_env: TEST_LEAK_KEY\n")

	cfg, _ := config.Load()
	err := runProviderTest(cfg, "custom", false, 0)
	if err == nil {
		t.Fatal("test against 401 succeeded, want failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("test error leaked key: %q", err.Error())
	}
}

func TestProviderTestRefusesMetadata(t *testing.T) {
	isolateProviderHome(t)
	writeProviderConfig(t, "model:\n  provider: bad\n  name: m\nproviders:\n  bad:\n    type: openai-compatible\n    base_url: http://169.254.169.254/v1\n")

	// Fail-closed at load: the active provider's metadata endpoint is
	// rejected before any probe or request is built.
	if _, err := config.Load(); err == nil {
		t.Fatal("Load() accepted a metadata endpoint without opt-in")
	} else if !strings.Contains(strings.ToLower(err.Error()), "metadata") {
		t.Errorf("error = %q, want metadata refusal", err)
	}
}
