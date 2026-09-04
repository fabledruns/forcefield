package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func isolateDoctorHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestDoctor_ProbeJSON(t *testing.T) {
	// Successful probe — valid JSON is decoded and important fields are present.
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer secret123" {
				t.Errorf("Authorization header = %q, want Bearer secret123", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"data":[{"id":"gpt-4o-mini"},{"id":"gpt-4o"}]}`)
		}))
		defer srv.Close()

		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		var reports []string
		report := func(v verdict, format string, args ...any) {
			reports = append(reports, fmt.Sprintf(format, args...))
		}
		auth := func(req *http.Request) { req.Header.Set("Authorization", "Bearer secret123") }
		ok := probeJSON(srv.Client(), srv.URL+"/models", auth, &body, report, "OpenAI")
		if !ok {
			t.Fatalf("probeJSON success returned false, reports: %v", reports)
		}
		if len(body.Data) != 2 || body.Data[0].ID != "gpt-4o-mini" {
			t.Fatalf("unexpected body: %+v", body)
		}
		// Ensure the secret was not emitted in any report (should be none on success).
		for _, r := range reports {
			if strings.Contains(r, "secret123") {
				t.Errorf("report leaked secret: %q", r)
			}
		}
		// Verify JSON is valid round-trip.
		if _, err := json.Marshal(body); err != nil {
			t.Fatalf("body not valid JSON: %v", err)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintln(w, `{"error":"unauthorized"}`)
		}))
		defer srv.Close()
		var body struct{}
		var reports []string
		report := func(v verdict, format string, args ...any) {
			if v != vFail {
				t.Errorf("expected vFail, got %v", v)
			}
			reports = append(reports, fmt.Sprintf(format, args...))
		}
		ok := probeJSON(srv.Client(), srv.URL, func(*http.Request) {}, &body, report, "TestService")
		if ok {
			t.Fatal("probeJSON unauthorized should return false")
		}
		if len(reports) == 0 || !strings.Contains(reports[0], "authentication failed") {
			t.Errorf("expected auth failure report, got %v", reports)
		}
		for _, r := range reports {
			if strings.Contains(r, "secret") {
				t.Errorf("report leaked secret: %q", r)
			}
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		var body struct{}
		report := func(v verdict, format string, args ...any) {
			if v != vFail {
				t.Errorf("expected vFail, got %v", v)
			}
		}
		ok := probeJSON(srv.Client(), srv.URL, func(*http.Request) {}, &body, report, "TestService")
		if ok {
			t.Fatal("probeJSON forbidden should return false")
		}
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		var body struct{}
		var sawFail bool
		report := func(v verdict, format string, args ...any) {
			if v == vFail {
				sawFail = true
			}
		}
		ok := probeJSON(srv.Client(), srv.URL, func(*http.Request) {}, &body, report, "TestService")
		if ok {
			t.Fatal("probeJSON 500 should return false")
		}
		if !sawFail {
			t.Error("expected vFail report for 500")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `not json at all`)
		}))
		defer srv.Close()
		var body struct {
			Data []string `json:"data"`
		}
		var sawFail bool
		report := func(v verdict, format string, args ...any) {
			if v == vFail {
				sawFail = true
			}
		}
		ok := probeJSON(srv.Client(), srv.URL, func(*http.Request) {}, &body, report, "TestService")
		if ok {
			t.Fatal("probeJSON malformed JSON should return false")
		}
		if !sawFail {
			t.Error("expected vFail for malformed JSON")
		}
	})

	t.Run("network error", func(t *testing.T) {
		// Use a server then close it to force connection error.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close()
		var body struct{}
		var sawFail bool
		report := func(v verdict, format string, args ...any) {
			if v == vFail {
				sawFail = true
			}
		}
		ok := probeJSON(srv.Client(), srv.URL, func(*http.Request) {}, &body, report, "Ollama")
		if ok {
			t.Fatal("probeJSON network error should return false")
		}
		if !sawFail {
			t.Error("expected vFail for network error")
		}
	})

	t.Run("secret not leaked on failure", func(t *testing.T) {
		secret := "nvapi-super-secret-do-not-log"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Echo that the server received the secret, but probeJSON should
			// never log it via report.
			if r.Header.Get("Authorization") != "Bearer "+secret {
				t.Errorf("expected auth header with secret")
			}
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		var body struct{}
		var reports []string
		report := func(v verdict, format string, args ...any) {
			reports = append(reports, fmt.Sprintf(format, args...))
		}
		auth := func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+secret) }
		probeJSON(srv.Client(), srv.URL, auth, &body, report, "TestService")
		for _, r := range reports {
			if strings.Contains(r, secret) {
				t.Errorf("report contained secret: %q", r)
			}
		}
	})
}

func TestDoctorConfig_ValidAndInvalid(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		isolateDoctorHome(t)
		if _, err := config.Load(); err != nil {
			t.Fatalf("config.Load: %v", err)
		}
		var reports []string
		report := func(v verdict, format string, args ...any) {
			reports = append(reports, fmt.Sprintf("%s %s", v.String(), fmt.Sprintf(format, args...)))
		}
		cfg := doctorConfig(report)
		if cfg == nil {
			t.Fatalf("doctorConfig returned nil for valid config")
		}
		found := false
		for _, r := range reports {
			if strings.Contains(r, "[ ok ]") && strings.Contains(r, "config:") {
				found = true
			}
			if strings.Contains(r, "secret") {
				t.Errorf("report leaked secret: %q", r)
			}
		}
		if !found {
			t.Errorf("expected ok config report, got %v", reports)
		}
	})

	t.Run("invalid config", func(t *testing.T) {
		home := isolateDoctorHome(t)
		path, err := config.Path()
		if err != nil {
			t.Fatalf("Path: %v", err)
		}
		// Ensure dir exists.
		_ = os.MkdirAll(filepath.Join(home, ".forcefield"), 0o700)
		if err := os.WriteFile(path, []byte("model: [unclosed"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		var sawFail bool
		report := func(v verdict, format string, args ...any) {
			if v == vFail {
				sawFail = true
			}
		}
		cfg := doctorConfig(report)
		if cfg != nil {
			t.Error("doctorConfig should return nil for invalid config")
		}
		if !sawFail {
			t.Error("expected vFail for invalid config")
		}
	})
}

func TestDoctorAPIKey_Reporting(t *testing.T) {
	t.Run("key missing", func(t *testing.T) {
		isolateDoctorHome(t)
		// Use non-auth local provider (ollama) to trigger early return.
		// For ollama, doctorAPIKey returns immediately (no auth required).
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		var reports []string
		report := func(v verdict, format string, args ...any) {
			reports = append(reports, fmt.Sprintf(format, args...))
		}
		doctorAPIKey(cfg, report)
		// Ollama has AuthNone, so no report expected.
		if len(reports) != 0 {
			t.Errorf("expected no report for ollama, got %v", reports)
		}
	})

	t.Run("nil config", func(t *testing.T) {
		// Should not panic when cfg is nil.
		var reports []string
		report := func(v verdict, format string, args ...any) {
			reports = append(reports, fmt.Sprintf(format, args...))
		}
		doctorAPIKey(nil, report)
		if len(reports) != 0 {
			t.Errorf("expected no report for nil cfg")
		}
	})

	t.Run("key set via env - value hidden", func(t *testing.T) {
		home := isolateDoctorHome(t)
		// Write config that uses an auth-required provider.
		path, err := config.Path()
		if err != nil {
			t.Fatalf("Path: %v", err)
		}
		_ = os.MkdirAll(filepath.Join(home, ".forcefield"), 0o700)
		body := "model:\n  provider: openai\n  name: gpt-4o-mini\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Setenv("OPENAI_API_KEY", "sk-test-12345")
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		var reports []string
		report := func(v verdict, format string, args ...any) {
			reports = append(reports, fmt.Sprintf(format, args...))
		}
		doctorAPIKey(cfg, report)
		if len(reports) == 0 {
			t.Fatal("expected report for openai key set")
		}
		for _, r := range reports {
			if strings.Contains(r, "sk-test-12345") {
				t.Errorf("report leaked API key: %q", r)
			}
			if !strings.Contains(r, "value hidden") {
				t.Errorf("report should say value hidden, got %q", r)
			}
		}
	})
}

func TestDoctorProvider_OpenAICompatible(t *testing.T) {
	// Successful openai-compatible probe via httptest server.
	home := isolateDoctorHome(t)
	homeForcefield := filepath.Join(home, ".forcefield")
	_ = os.MkdirAll(homeForcefield, 0o700)

	// Start a fake OpenAI server that serves /models.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Errorf("expected Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"data":[{"id":"gpt-4o-mini"},{"id":"gpt-4o"}]}`)
	}))
	defer srv.Close()

	body := "model:\n  provider: openai\n  name: gpt-4o-mini\nproviders:\n  openai:\n    type: openai-compatible\n    base_url: " + srv.URL + "\n    api_key_env: OPENAI_API_KEY\n"
	path, err := config.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "sk-fake-secret")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var reports []string
	report := func(v verdict, format string, args ...any) {
		reports = append(reports, fmt.Sprintf("%s %s", v.String(), fmt.Sprintf(format, args...)))
	}
	doctorProvider(cfg, report)
	foundOK := false
	for _, r := range reports {
		if strings.Contains(r, "server is up") {
			foundOK = true
		}
		if strings.Contains(r, "sk-fake-secret") {
			t.Errorf("report leaked secret: %q", r)
		}
	}
	if !foundOK {
		t.Errorf("expected server is up report, got %v", reports)
	}
}

func TestDoctorProvider_OpenCodeGo(t *testing.T) {
	// OpenCode gateways probe GET /models with Bearer auth.
	home := isolateDoctorHome(t)
	homeForcefield := filepath.Join(home, ".forcefield")
	_ = os.MkdirAll(homeForcefield, 0o700)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-fake-secret" {
			t.Errorf("auth = %q, want Bearer key", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"data":[{"id":"glm-5.3"}]}`)
	}))
	defer srv.Close()

	body := "model:\n  provider: opencode-go\n  name: glm-5.3\nproviders:\n  opencode-go:\n    type: opencode-go\n    base_url: " + srv.URL + "\n    api_key_env: OPENCODE_API_KEY\n"
	path, err := config.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("OPENCODE_API_KEY", "sk-fake-secret")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var reports []string
	report := func(v verdict, format string, args ...any) {
		reports = append(reports, fmt.Sprintf("%s %s", v.String(), fmt.Sprintf(format, args...)))
	}
	doctorProvider(cfg, report)
	foundOK := false
	for _, r := range reports {
		if strings.Contains(r, "server is up") {
			foundOK = true
		}
		if strings.Contains(r, "sk-fake-secret") {
			t.Errorf("report leaked secret: %q", r)
		}
	}
	if !foundOK {
		t.Errorf("expected server is up report, got %v", reports)
	}
}

func TestDoctorProvider_SkipsWhenKeyMissing(t *testing.T) {
	home := isolateDoctorHome(t)
	_ = os.MkdirAll(filepath.Join(home, ".forcefield"), 0o700)
	path, err := config.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	body := "model:\n  provider: openai\n  name: gpt-4o-mini\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Ensure no key set.
	t.Setenv("OPENAI_API_KEY", "")
	// Also ensure no .env file provides it.
	_ = os.Remove(filepath.Join(home, ".forcefield", ".env"))
	_ = os.Remove(filepath.Join(".", ".env"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var reports []string
	report := func(v verdict, format string, args ...any) {
		reports = append(reports, fmt.Sprintf(format, args...))
	}
	doctorProvider(cfg, report)
	foundWarn := false
	for _, r := range reports {
		if strings.Contains(r, "skipping reachability probe") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected skip warning when key missing, got %v", reports)
	}
}

func TestOrNone(t *testing.T) {
	if got := orNone(nil); got != "(none)" {
		t.Errorf("orNone(nil) = %q, want (none)", got)
	}
	if got := orNone([]string{}); got != "(none)" {
		t.Errorf("orNone([]) = %q, want (none)", got)
	}
	if got := orNone([]string{"a", "b"}); got != "a, b" {
		t.Errorf("orNone(a,b) = %q, want a, b", got)
	}
}

func TestVerdictString(t *testing.T) {
	cases := map[verdict]string{
		vOK:   "[ ok ]",
		vWarn: "[warn]",
		vFail: "[FAIL]",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Errorf("verdict %d String = %q, want %q", v, got, want)
		}
	}
}

func TestDoctorSessionsAndSkillsAndMemory(t *testing.T) {
	// Exercise doctorSessions, doctorSkills, doctorMemory without real home corruption.
	home := isolateDoctorHome(t)
	_ = os.MkdirAll(filepath.Join(home, ".forcefield"), 0o700)
	if _, err := config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	var reports []string
	report := func(v verdict, format string, args ...any) {
		reports = append(reports, fmt.Sprintf(format, args...))
	}
	doctorSessions(report)
	doctorSkills(report)
	doctorMemory(report)
	// All should produce at least an ok or warn report, not panic.
	if len(reports) < 3 {
		t.Errorf("expected at least 3 reports from sessions/skills/memory, got %d: %v", len(reports), reports)
	}
}

func TestDoctorSandbox_NilConfig(t *testing.T) {
	var reports []string
	report := func(v verdict, format string, args ...any) {
		reports = append(reports, fmt.Sprintf(format, args...))
	}
	// Should not panic and should report nothing when cfg is nil.
	doctorSandbox(nil, report)
	if len(reports) != 0 {
		t.Errorf("expected no reports for nil cfg, got %v", reports)
	}
}

func TestNewSandboxExecutor_InvalidMode(t *testing.T) {
	home := isolateDoctorHome(t)
	_ = os.MkdirAll(filepath.Join(home, ".forcefield"), 0o700)
	path, err := config.Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	body := "model:\n  provider: ollama\n  endpoint: http://localhost:11434\n  name: ornith:9b\nsandbox:\n  mode: invalid\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := config.Load(); err == nil {
		t.Fatal("expected Load to fail for invalid sandbox.mode, but it succeeded")
	}
	// Even with a manually constructed config, newSandboxExecutor should fail.
	cfg2 := &config.Config{Sandbox: config.Sandbox{Mode: "invalid"}}
	if _, err := newSandboxExecutor(cfg2); err == nil {
		t.Error("newSandboxExecutor with invalid mode should error")
	}
	_ = home
}
