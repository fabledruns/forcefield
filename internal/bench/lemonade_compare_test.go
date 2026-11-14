package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/config"
)

// NOTE: These tests use deterministic httptest.NewServer mocks to validate
// benchmark harness plumbing, SlotGuard concurrency bounding, environment
// probing, telemetry capture, and 3-way A/B/C comparison reporting.
// They are NOT real LLM inference benchmarks and do not produce real latency
// or throughput numbers; see bench/lemonade/ for live benchmark scripts.

type mockComparisonServer struct {
	srv         *httptest.Server
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	chatCalls   atomic.Int32
}

func newMockComparisonServer(t *testing.T) *mockComparisonServer {
	t.Helper()
	m := &mockComparisonServer{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"status":"ok",
				"model_loaded":"qwen-coder-mock",
				"all_models_loaded":[{
					"model_name":"qwen-coder-mock",
					"recipe":"llamacpp",
					"device":"gpu",
					"vram_usage":2.1,
					"checkpoint":"Qwen/Qwen2.5-Coder-3B-Instruct-GGUF:Q4_K_M",
					"recipe_options":{
						"ctx_size":16384,
						"llamacpp_args":"-np 1"
					}
				}]
			}`)
			return
		case "/api/v1/slots":
			w.Header().Set("Content-Type", "application/json")
			busy := m.inFlight.Load() > 0
			_, _ = fmt.Fprintf(w, `[{"id":0,"is_processing":%t}]`, busy)
			return
		case "/api/v1/chat/completions", "/chat/completions":
			cur := m.inFlight.Add(1)
			defer m.inFlight.Add(-1)
			for {
				prev := m.maxInFlight.Load()
				if cur <= prev || m.maxInFlight.CompareAndSwap(prev, cur) {
					break
				}
			}
			m.chatCalls.Add(1)

			bodyBytes, _ := io.ReadAll(r.Body)
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(bodyBytes, &req)

			time.Sleep(25 * time.Millisecond)

			hasToolResult := false
			lastUser := ""
			for _, msg := range req.Messages {
				if msg.Role == "tool" {
					hasToolResult = true
				}
				if msg.Role == "user" {
					lastUser = msg.Content
				}
			}

			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)

			writeChunk := func(payload string) {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
				if flusher != nil {
					flusher.Flush()
				}
			}

			switch {
			case strings.Contains(lastUser, "delegate tool") && !hasToolResult:
				// Parent Turn 1: dispatch two concurrent child delegations.
				tc0Args := mustJSONString(map[string]any{
					"agent": "coding",
					"task":  "Update crypto/hash.go ComputeHash to return hex-encoded SHA-256.",
				})
				tc1Args := mustJSONString(map[string]any{
					"agent": "coding",
					"task":  "Update auth/token.go GenerateToken(secret, userID string) to accept explicit secret.",
				})
				writeChunk(fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_crypto","type":"function","function":{"name":"delegate","arguments":%s}},{"index":1,"id":"call_auth","type":"function","function":{"name":"delegate","arguments":%s}}]}}]}`, mustJSONString(tc0Args), mustJSONString(tc1Args)))
				writeChunk(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
			case strings.Contains(lastUser, "crypto/hash.go") && !hasToolResult:
				// Child 1 Turn 1: write crypto/hash.go.
				wfArgs := mustJSONString(map[string]any{
					"path":    "crypto/hash.go",
					"content": "package crypto\n\nimport (\n\t\"crypto/sha256\"\n\t\"fmt\"\n)\n\nfunc ComputeHash(data []byte) string {\n\tsum := sha256.Sum256(data)\n\treturn fmt.Sprintf(\"%x\", sum)\n}\n",
				})
				writeChunk(fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_wf_crypto","type":"function","function":{"name":"write_file","arguments":%s}}]}}]}`, mustJSONString(wfArgs)))
				writeChunk(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
			case strings.Contains(lastUser, "auth/token.go") && !hasToolResult:
				// Child 2 Turn 1: write auth/token.go.
				wfArgs := mustJSONString(map[string]any{
					"path":    "auth/token.go",
					"content": "package auth\n\nimport \"fmt\"\n\nfunc GenerateToken(secret, userID string) string {\n\treturn fmt.Sprintf(\"token:%s:%s\", userID, secret)\n}\n",
				})
				writeChunk(fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_wf_auth","type":"function","function":{"name":"write_file","arguments":%s}}]}}]}`, mustJSONString(wfArgs)))
				writeChunk(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
			default:
				// Child Turn 2 or Parent Turn 2: final answer.
				writeChunk(`{"choices":[{"delta":{"content":"Hardening complete."}}]}`)
				writeChunk(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			}

			writeChunk(`{"usage":{"prompt_tokens":120,"completion_tokens":32,"total_tokens":152,"prompt_tokens_details":{"cached_tokens":64}},"timings":{"cache_n":64,"prompt_n":56,"prompt_ms":42.5,"prompt_per_second":1317.6,"predicted_n":32,"predicted_ms":512.0,"predicted_per_second":62.5}}`)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func mustJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func writeDelegBenchTask(t *testing.T, tasksDir string) {
	t.Helper()
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("tasks", "DELEG-001.yaml"))
	if err != nil {
		t.Fatalf("read DELEG-001.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "DELEG-001.yaml"), src, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBenchmark_ThreeWayComparison_MockHarness(t *testing.T) {
	tasksDir := filepath.Join(t.TempDir(), "tasks")
	writeDelegBenchTask(t, tasksDir)

	runMode := func(name string, providerType string, disableSlotGuard bool) (*RunResult, *mockComparisonServer) {
		srv := newMockComparisonServer(t)
		resultsDir := filepath.Join(t.TempDir(), "results-"+name)

		cfg := &config.Config{
			Model: config.Model{
				Provider: name,
				Name:     "qwen-coder-mock",
			},
			Providers: map[string]config.ProviderConfig{
				name: {
					Type:             providerType,
					BaseURL:          srv.srv.URL,
					DisableSlotGuard: disableSlotGuard,
				},
			},
		}

		runner := NewRunner(RunnerOptions{
			TasksDir:   tasksDir,
			ResultsDir: resultsDir,
			TaskFilter: "DELEG-001",
			BaseConfig: cfg,
		})
		res, err := runner.Run(context.Background())
		if err != nil {
			t.Fatalf("%s: runner.Run failed: %v", name, err)
		}
		if len(res.Tasks) != 1 || !res.Tasks[0].Passed {
			t.Fatalf("%s: expected DELEG-001 to pass, got %+v", name, res.Tasks)
		}
		return res, srv
	}

	// Mode A: Existing provider baseline (openai-compatible, no SlotGuard, no /health probe).
	baseRes, baseSrv := runMode("baseline-openai", "openai-compatible", false)
	if baseRes.Environment == nil || baseRes.Environment.ProviderType != "openai-compatible" || baseRes.Environment.SlotGuardMode != "n/a" {
		t.Fatalf("unexpected baseline environment: %+v", baseRes.Environment)
	}
	// Default openai-compatible catalog capabilities keep ParallelToolCalls=false,
	// so the scheduler runs sibling tool calls sequentially; verify it still
	// received chat calls while reporting neither SlotGuard nor Lemonade timings.
	if got := baseSrv.chatCalls.Load(); got < 2 {
		t.Fatalf("baseline chatCalls = %d, expected >= 2", got)
	}
	if baseRes.Tasks[0].SlotGuard != nil {
		t.Fatalf("expected nil SlotGuard on openai-compatible baseline, got %+v", baseRes.Tasks[0].SlotGuard)
	}
	if baseRes.Tasks[0].Inference != nil {
		t.Fatalf("expected nil Inference timings on openai-compatible baseline, got %+v", baseRes.Tasks[0].Inference)
	}

	// Mode B: Lemonade without SlotGuard (disable_slot_guard: true).
	noGuardRes, noGuardSrv := runMode("lemonade-unguarded", "lemonade", true)
	if noGuardRes.Environment == nil {
		t.Fatal("expected Environment on lemonade-unguarded")
	}
	if noGuardRes.Environment.ProviderType != "lemonade" ||
		noGuardRes.Environment.Recipe != "llamacpp" ||
		!strings.HasPrefix(noGuardRes.Environment.Backend, "llamacpp") ||
		noGuardRes.Environment.Quantization != "Q4_K_M" ||
		noGuardRes.Environment.ContextSize != 16384 ||
		noGuardRes.Environment.ParallelSlots != 1 ||
		noGuardRes.Environment.SlotGuardMode != "disabled" ||
		!noGuardRes.Environment.LemonadeLive {
		t.Fatalf("unexpected lemonade-unguarded environment: %+v", noGuardRes.Environment)
	}
	if got := noGuardSrv.maxInFlight.Load(); got < 2 {
		t.Fatalf("lemonade-unguarded maxInFlight = %d, expected >= 2 concurrent requests when SlotGuard disabled", got)
	}
	if noGuardRes.Tasks[0].SlotGuard != nil {
		t.Fatalf("expected nil SlotGuard when disabled, got %+v", noGuardRes.Tasks[0].SlotGuard)
	}
	if noGuardRes.Tasks[0].Inference == nil || noGuardRes.Tasks[0].Inference.PredictedPerSecond <= 0 {
		t.Fatalf("expected non-zero Inference timings on lemonade-unguarded, got %+v", noGuardRes.Tasks[0].Inference)
	}
	if noGuardRes.Tasks[0].Tokens.CachedTokens == 0 {
		t.Fatalf("expected CachedTokens > 0 on lemonade-unguarded, got %+v", noGuardRes.Tasks[0].Tokens)
	}

	// Mode C: Lemonade with SlotGuard enabled (disable_slot_guard: false).
	guardRes, guardSrv := runMode("lemonade-slotguard", "lemonade", false)
	if guardRes.Environment == nil || guardRes.Environment.SlotGuardMode != "enabled" {
		t.Fatalf("unexpected lemonade-slotguard environment: %+v", guardRes.Environment)
	}
	if got := guardSrv.maxInFlight.Load(); got != 1 {
		t.Fatalf("lemonade-slotguard maxInFlight = %d, want 1 (SlotGuard must serialize concurrent children to -np 1)", got)
	}
	sg := guardRes.Tasks[0].SlotGuard
	if sg == nil || !sg.Enabled || sg.Admitted < 5 || sg.Waits < 1 {
		t.Fatalf("expected SlotGuard telemetry with Enabled=true, Admitted>=5, Waits>=1, got %+v", sg)
	}
	if guardRes.Summary.SlotGuardAdmitted < 5 || guardRes.Summary.SlotGuardWaits < 1 {
		t.Fatalf("expected Summary SlotGuard totals populated, got %+v", guardRes.Summary)
	}
	if guardRes.Summary.LatencyP50MS <= 0 || guardRes.Summary.LatencyP95MS <= 0 || guardRes.Summary.AvgPredictedPerSecond <= 0 {
		t.Fatalf("expected Summary latency/throughput populated, got %+v", guardRes.Summary)
	}

	// Verify saved JSON and Markdown reports preserve environment and SlotGuard fields.
	jsonBytes, err := os.ReadFile(guardRes.JSONReportPath)
	if err != nil {
		t.Fatalf("read JSON report: %v", err)
	}
	var parsed JSONReport
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("unmarshal JSON report: %v", err)
	}
	if parsed.Environment == nil || parsed.Environment.Quantization != "Q4_K_M" || parsed.Environment.SlotGuardMode != "enabled" {
		t.Fatalf("JSON report missing Environment metadata: %+v", parsed.Environment)
	}
	mdBytes, err := os.ReadFile(guardRes.MDReportPath)
	if err != nil {
		t.Fatalf("read MD report: %v", err)
	}
	md := string(mdBytes)
	for _, want := range []string{
		"## Run Metadata",
		"llamacpp:gpu",
		"Q4_K_M",
		"16384 tokens",
		"**Parallel Slots (`-np`)**",
		"**SlotGuard**",
		"**SlotGuard (Admitted / Waits / Wait Time)**",
		"**Avg Throughput (Prompt / Gen)**",
		"**Latency (p50 / p95)**",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown report missing %q:\n%s", want, md)
		}
	}
}
