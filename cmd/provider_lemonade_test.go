package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func TestProviderTestLemonade_ModelsHealthOptionsAndChat(t *testing.T) {
	var sawChat bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/models" && r.URL.Query().Get("show_all") == "true":
			fmt.Fprint(w, `{"data":[{"id":"Qwen2.5-Coder-3B","downloaded":true},{"id":"Not-Downloaded","downloaded":false}]}`)
		case r.URL.Path == "/api/v1/health":
			fmt.Fprint(w, `{"status":"ok","all_models_loaded":[{"model_name":"Qwen2.5-Coder-3B","recipe":"llamacpp","device":"gpu","pinned":false,"launch_command":["llama-server","-np","1"]}]}`)
		case r.URL.Path == "/api/v1/models/Qwen2.5-Coder-3B/options":
			fmt.Fprint(w, `{"resolved_ctx_size":8192}`)
		case r.URL.Path == "/v1/chat/completions":
			sawChat = true
			if r.Method != http.MethodPost {
				t.Errorf("chat probe method = %s, want POST", r.Method)
			}
			var body struct {
				Model     string `json:"model"`
				MaxTokens int    `json:"max_tokens"`
				Stream    bool   `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode chat probe request body: %v", err)
			}
			if body.Model != "Qwen2.5-Coder-3B" {
				t.Errorf("chat probe model = %q, want Qwen2.5-Coder-3B", body.Model)
			}
			if body.MaxTokens != 1 {
				t.Errorf("chat probe max_tokens = %d, want 1", body.MaxTokens)
			}
			if body.Stream {
				t.Errorf("chat probe stream = %v, want false", body.Stream)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"},"finish_reason":"stop"}]}`)
		case r.URL.Path == "/api/v1/chat" || r.URL.Path == "/api/v1/chat/completions":
			t.Errorf("chat probe requested disallowed management endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	isolateProviderHome(t)
	writeProviderConfig(t, fmt.Sprintf(`model:
  provider: lemonade
  name: Qwen2.5-Coder-3B
providers:
  lemonade:
    type: lemonade
    base_url: %s
`, srv.URL))

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if err := runProviderTest(cfg, "lemonade", true, 5); err != nil {
		t.Fatalf("runProviderTest(lemonade, chat=true) error = %v", err)
	}
	if !sawChat {
		t.Error("expected --chat probe to hit /v1/chat/completions")
	}

	// Verify secret redaction in error path when server rejects auth.
	secret := "sk-lem-test-secret-999"
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":"bad key %s"}`, secret)
	}))
	defer authSrv.Close()

	t.Setenv("LEMON_TEST_KEY", secret)
	writeProviderConfig(t, fmt.Sprintf(`model:
  provider: mylemon
  name: Qwen2.5-Coder-3B
providers:
  mylemon:
    type: lemonade
    base_url: %s/api/v1
    api_key_env: LEMON_TEST_KEY
`, authSrv.URL))
	cfg, _ = config.Load()
	err = runProviderTest(cfg, "mylemon", false, 5)
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("provider test error leaked secret: %q", err.Error())
	}
}

func TestProviderTestLemonade_ChatProbeEndpointNormalization(t *testing.T) {
	variants := []string{
		"",
		"/",
		"/api/v1",
		"/api/v1/",
		"/v1",
		"/v1/",
		"/api/v1/chat",
	}

	for _, v := range variants {
		t.Run("variant"+strings.ReplaceAll(v, "/", "_"), func(t *testing.T) {
			var hitChat bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/models") && r.URL.Query().Get("show_all") == "true":
					fmt.Fprint(w, `{"data":[{"id":"TestModel-GGUF","downloaded":true}]}`)
				case strings.HasSuffix(r.URL.Path, "/health"):
					fmt.Fprint(w, `{"status":"ok","all_models_loaded":[]}`)
				case r.URL.Path == "/v1/chat/completions":
					hitChat = true
					var payload struct {
						Model     string `json:"model"`
						MaxTokens int    `json:"max_tokens"`
					}
					bodyBytes, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(bodyBytes, &payload)
					if payload.Model != "TestModel-GGUF" {
						t.Errorf("model = %q, want TestModel-GGUF", payload.Model)
					}
					if payload.MaxTokens != 1 {
						t.Errorf("max_tokens = %d, want 1", payload.MaxTokens)
					}
					fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}]}`)
				case r.Method == http.MethodPost && (r.URL.Path == "/api/v1/chat" || r.URL.Path == "/api/v1/chat/completions"):
					t.Errorf("chat probe requested disallowed path: %s", r.URL.Path)
					http.NotFound(w, r)
				case strings.Contains(r.URL.Path, "/v1/v1"):
					t.Errorf("probe requested duplicated path segment: %s", r.URL.Path)
					http.NotFound(w, r)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			isolateProviderHome(t)
			writeProviderConfig(t, fmt.Sprintf(`model:
  provider: lemonade
  name: TestModel-GGUF
providers:
  lemonade:
    type: lemonade
    base_url: %s%s
`, srv.URL, v))

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load() error = %v", err)
			}
			if err := runProviderTest(cfg, "lemonade", true, 5); err != nil {
				t.Fatalf("runProviderTest(lemonade, chat=true) for %s error = %v", v, err)
			}
			if !hitChat {
				t.Errorf("endpoint variant %q did not hit /v1/chat/completions", v)
			}
		})
	}
}

func TestProviderTestLemonade_ChatProbeActionable404(t *testing.T) {
	tests := []struct {
		name       string
		respBody   string
		wantSubstr string
	}{
		{
			name:       "json_error_message",
			respBody:   `{"error":{"code":"model_not_found","message":"Model 'Qwen3-1.7B-GGUF' was not found. Use 'lemonade list' to see all available models.","type":"model_not_found"}}`,
			wantSubstr: "Model 'Qwen3-1.7B-GGUF' was not found",
		},
		{
			name:       "plain_text_404",
			respBody:   `The requested endpoint does not exist`,
			wantSubstr: "The requested endpoint does not exist",
		},
		{
			name:       "empty_body_actionable_hint",
			respBody:   ``,
			wantSubstr: "model \"Qwen3-1.7B-GGUF\" was not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/models"):
					fmt.Fprint(w, `{"data":[{"id":"Qwen3-1.7B-GGUF","downloaded":true}]}`)
				case strings.HasSuffix(r.URL.Path, "/health"):
					fmt.Fprint(w, `{"status":"ok","all_models_loaded":[]}`)
				case r.URL.Path == "/v1/chat/completions":
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, tc.respBody)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			isolateProviderHome(t)
			writeProviderConfig(t, fmt.Sprintf(`model:
  provider: lemonade
  name: Qwen3-1.7B-GGUF
providers:
  lemonade:
    type: lemonade
    base_url: %s
`, srv.URL))

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load() error = %v", err)
			}
			err = runProviderTest(cfg, "lemonade", true, 5)
			if err == nil {
				t.Fatal("expected chat probe failure on 404, got nil")
			}
			if !strings.Contains(err.Error(), "HTTP 404") {
				t.Errorf("error %q should mention HTTP 404", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}
