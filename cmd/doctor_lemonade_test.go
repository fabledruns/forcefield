package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"strings"
	"testing"

	"forcefield/internal/config"
)

func TestDoctorLemonade_HealthyGPU(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/live":
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/api/v1/health":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok",
				"all_models_loaded": []map[string]any{{
					"model_name":         "Qwen2.5-Coder-3B-Instruct-GGUF",
					"recipe":             "llamacpp",
					"device":             "gpu",
					"status":             "ready",
					"pinned":             false,
					"max_context_window": 8192,
					"launch_command":     []string{"llama-server", "--parallel", "1"},
				}},
				"pinned_models": map[string]int{"llm": 0},
				"max_models":    map[string]int{"llm": 1},
			})
		case "/api/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"Qwen2.5-Coder-3B-Instruct-GGUF","recipe":"llamacpp","downloaded":true}]}`)
		case "/api/v1/models/Qwen2.5-Coder-3B-Instruct-GGUF/options":
			fmt.Fprint(w, `{"resolved_ctx_size":8192}`)
		case "/api/v1/system-info":
			fmt.Fprint(w, `{"recipes":{"llamacpp":{"backends":{"cuda":{}}}}}`)
		case "/api/v1/system-stats":
			fmt.Fprint(w, `{"gpus":[{"name":"RTX 3050","memory_used_gb":2.1,"memory_total_gb":4.0}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	isolateProviderHome(t)
	writeProviderConfig(t, fmt.Sprintf(`model:
  provider: lemonade
  name: Qwen2.5-Coder-3B-Instruct-GGUF
providers:
  lemonade:
    type: lemonade
    base_url: %s/api/v1
`, srv.URL))

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	var lines []string
	var fails int
	var unexpectedWarns []string
	doctorProvider(cfg, func(v verdict, format string, args ...any) {
		line := doctorLine(v, format, args...)
		lines = append(lines, line)
		if v == vFail {
			fails++
		}
		if v == vWarn && !strings.Contains(line, "fine for local inference servers") {
			unexpectedWarns = append(unexpectedWarns, line)
		}
	})

	joined := strings.Join(lines, "")
	if fails != 0 || len(unexpectedWarns) != 0 {
		t.Fatalf("unexpected fails=%d warns=%v in healthy GPU output:\n%s", fails, unexpectedWarns, joined)
	}
	for _, want := range []string{"server is live", "device=gpu", "np=1", "resolved context window", "RTX 3050"} {
		if !strings.Contains(joined, want) {
			t.Errorf("doctor output missing %q:\n%s", want, joined)
		}
	}
}

func TestDoctorLemonade_WarningsAndFailures(t *testing.T) {
	t.Run("cpu_fallback_and_high_vram_and_pinned_saturation", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/live":
				fmt.Fprint(w, `{"status":"ok"}`)
			case "/api/v1/health":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "ok",
					"all_models_loaded": []map[string]any{{
						"model_name":         "Other-Pinned-Model",
						"recipe":             "llamacpp",
						"device":             "gpu",
						"status":             "ready",
						"pinned":             true,
						"max_context_window": 4096,
					}, {
						"model_name":         "Qwen-CPU",
						"recipe":             "llamacpp",
						"device":             "cpu",
						"status":             "ready",
						"pinned":             false,
						"max_context_window": 2048,
					}},
					"pinned_models": map[string]int{"llm": 1},
					"max_models":    map[string]int{"llm": 1},
				})
			case "/api/v1/models":
				fmt.Fprint(w, `{"data":[{"id":"Unpulled-Model","downloaded":false}]}`)
			case "/api/v1/system-info":
				fmt.Fprint(w, `{"recipes":{"llamacpp":{"backends":{"cuda":{}}}}}`)
			case "/api/v1/system-stats":
				fmt.Fprint(w, `{"gpus":[{"name":"RTX 3050","memory_used_gb":3.8,"memory_total_gb":4.0}]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		isolateProviderHome(t)
		writeProviderConfig(t, fmt.Sprintf(`model:
  provider: lemonade
  name: Unpulled-Model
providers:
  lemonade:
    type: lemonade
    base_url: %s/api/v1
`, srv.URL))

		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		var sawFail, sawPinnedWarn, sawVRAMWarn bool
		doctorProvider(cfg, func(v verdict, format string, args ...any) {
			line := doctorLine(v, format, args...)
			out = append(out, line)
			if v == vFail && strings.Contains(line, "not downloaded locally") {
				sawFail = true
			}
			if v == vWarn && strings.Contains(line, "pinned LLM slots saturated") {
				sawPinnedWarn = true
			}
			if v == vWarn && strings.Contains(line, "VRAM usage is high") {
				sawVRAMWarn = true
			}
		})
		joined := strings.Join(out, "")
		if !sawFail || !sawPinnedWarn || !sawVRAMWarn {
			t.Fatalf("missing expected diagnostics (fail=%v pinned=%v vram=%v):\n%s",
				sawFail, sawPinnedWarn, sawVRAMWarn, joined)
		}
	})

	t.Run("cpu_fallback_and_low_ctx_reserve", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/live":
				fmt.Fprint(w, `{"status":"ok"}`)
			case "/api/v1/health":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "ok",
					"all_models_loaded": []map[string]any{{
						"model_name":         "Qwen-CPU",
						"recipe":             "llamacpp",
						"device":             "cpu",
						"status":             "ready",
						"max_context_window": 2048,
					}},
					"pinned_models": map[string]int{"llm": 0},
					"max_models":    map[string]int{"llm": 1},
				})
			case "/api/v1/models":
				fmt.Fprint(w, `{"data":[{"id":"Qwen-CPU","downloaded":true}]}`)
			case "/api/v1/models/Qwen-CPU/options":
				fmt.Fprint(w, `{"resolved_ctx_size":2048}`)
			case "/api/v1/system-info":
				fmt.Fprint(w, `{"recipes":{"llamacpp":{"backends":{"vulkan":{}}}}}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		isolateProviderHome(t)
		writeProviderConfig(t, fmt.Sprintf(`model:
  provider: lemonade
  name: Qwen-CPU
providers:
  lemonade:
    type: lemonade
    base_url: %s/api/v1
`, srv.URL))
		cfg, _ := config.Load()

		var lines []string
		doctorProvider(cfg, func(v verdict, format string, args ...any) {
			lines = append(lines, doctorLine(v, format, args...))
		})
		joined := strings.Join(lines, "")
		if !strings.Contains(joined, "running on CPU") || !strings.Contains(joined, "GPU backends detected (vulkan)") || !strings.Contains(joined, "does not exceed reply reserve") {
			t.Errorf("expected CPU fallback + vulkan + ctx reserve warnings, got:\n%s", joined)
		}
	})

	t.Run("offline_wsl2_hint", func(t *testing.T) {
		if goruntime.GOOS != "linux" {
			t.Skip("WSL2 host detection applies only when running inside Linux/WSL2; native Windows behavior is verified in TestDoctorLemonade_OfflineWindowsNoWSL2Hint and offline_wsl2_hint_wiring")
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		closedURL := srv.URL
		srv.Close()

		t.Setenv("WSL_DISTRO_NAME", "Ubuntu-26.04")
		isolateProviderHome(t)
		writeProviderConfig(t, fmt.Sprintf(`model:
  provider: lemonade
  name: Qwen3-4B
providers:
  lemonade:
    type: lemonade
    base_url: %s/api/v1
`, closedURL))
		cfg, _ := config.Load()

		var lines []string
		doctorProvider(cfg, func(v verdict, format string, args ...any) {
			lines = append(lines, doctorLine(v, format, args...))
		})
		joined := strings.Join(lines, "")
		if !strings.Contains(joined, "[FAIL]") || !strings.Contains(joined, "networkingMode=mirrored") {
			t.Errorf("expected FAIL + WSL2 mirrored hint, got:\n%s", joined)
		}
	})

	t.Run("offline_wsl2_hint_wiring", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		closedURL := srv.URL
		srv.Close()

		origHint := wsl2LoopbackHint
		defer func() { wsl2LoopbackHint = origHint }()
		wsl2LoopbackHint = func(targetURL string, err error) string {
			if err == nil {
				return ""
			}
			return fmt.Sprintf("WSL2 note: %s is unreachable from WSL2. Enable networkingMode=mirrored in .wslconfig", targetURL)
		}

		isolateProviderHome(t)
		writeProviderConfig(t, fmt.Sprintf(`model:
  provider: lemonade
  name: Qwen3-4B
providers:
  lemonade:
    type: lemonade
    base_url: %s/api/v1
`, closedURL))
		cfg, _ := config.Load()

		var lines []string
		doctorProvider(cfg, func(v verdict, format string, args ...any) {
			lines = append(lines, doctorLine(v, format, args...))
		})
		joined := strings.Join(lines, "")
		if !strings.Contains(joined, "[FAIL]") || !strings.Contains(joined, "[warn]") || !strings.Contains(joined, "networkingMode=mirrored") {
			t.Errorf("expected FAIL + WARN WSL2 mirrored hint, got:\n%s", joined)
		}
	})
}
