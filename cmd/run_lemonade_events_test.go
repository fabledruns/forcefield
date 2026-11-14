package cmd

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
	"testing"
	"time"

	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

func TestRunHeadless_LemonadeSlotGuardAndTimingsTelemetry(t *testing.T) {
	origWriter := newEventWriter
	defer func() { newEventWriter = origWriter }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok","model_loaded":"qwen-mock","all_models_loaded":[{"model_name":"qwen-mock","recipe":"llamacpp","recipe_options":{"ctx_size":8192,"llamacpp_args":"-np 1"}}]}`)
		case "/api/v1/slots":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"id":0,"is_processing":false}]`)
		case "/api/v1/chat/completions":
			bodyBytes, _ := io.ReadAll(r.Body)
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(bodyBytes, &req)
			time.Sleep(15 * time.Millisecond)

			hasTool := false
			lastUser := ""
			for _, m := range req.Messages {
				if m.Role == "tool" {
					hasTool = true
				}
				if m.Role == "user" {
					lastUser = m.Content
				}
			}

			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			writeChunk := func(p string) {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", p)
				if flusher != nil {
					flusher.Flush()
				}
			}

			if strings.Contains(lastUser, "delegate check") && !hasTool {
				argsJSON, _ := json.Marshal(map[string]any{
					"agent": "research",
					"task":  "Return child canary acknowledgement.",
				})
				argsQuoted, _ := json.Marshal(string(argsJSON))
				writeChunk(fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_del_1","type":"function","function":{"name":"delegate","arguments":%s}}]}}]}`, string(argsQuoted)))
				writeChunk(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
			} else if strings.Contains(lastUser, "child canary") {
				writeChunk(`{"choices":[{"delta":{"content":"CHILD_CANARY_RESPONSE_TEXT"}}]}`)
				writeChunk(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			} else {
				writeChunk(`{"choices":[{"delta":{"content":"PARENT_CANARY_RESPONSE_TEXT"}}]}`)
				writeChunk(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			}
			writeChunk(`{"usage":{"prompt_tokens":80,"completion_tokens":20,"total_tokens":100,"prompt_tokens_details":{"cached_tokens":48}},"timings":{"cache_n":48,"prompt_n":32,"prompt_ms":20.0,"prompt_per_second":1600.0,"predicted_n":20,"predicted_ms":400.0,"predicted_per_second":50.0}}`)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	isolateResumeHome(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".forcefield"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgYAML := fmt.Sprintf("model:\n  provider: lemon\n  name: qwen-mock\norchestration:\n  enabled: true\npermissions:\n  default: allow\nproviders:\n  lemon:\n    type: lemonade\n    base_url: %s\n", srv.URL)
	if err := os.WriteFile(filepath.Join(home, ".forcefield", "config.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	rt, err := runtime.New()
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	var logPath string
	newEventWriter = func(path, runID string) (*orchevents.Writer, error) {
		logPath = path
		return orchevents.NewWriter(path, "run-lemonade-telemetry")
	}

	resp, status, _, runErr := runHeadlessSession(context.Background(), rt, []providers.Message{
		{Role: providers.UserRole, Content: "Run delegate check"},
	})
	if runErr != nil {
		t.Fatalf("runHeadlessSession error: %v", runErr)
	}
	if status != runtime.StatusVerified && status != runtime.StatusPartial {
		t.Fatalf("status = %q, want verified or partial", status)
	}
	if resp.Content != "PARENT_CANARY_RESPONSE_TEXT" {
		t.Fatalf("unexpected response content: %q", resp.Content)
	}

	rawBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	raw := string(rawBytes)
	// Privacy contract: response text must never enter .events.jsonl.
	if strings.Contains(raw, "PARENT_CANARY_RESPONSE_TEXT") || strings.Contains(raw, "CHILD_CANARY_RESPONSE_TEXT") {
		t.Fatalf("privacy leak: response content found in event log:\n%s", raw)
	}

	events, err := orchevents.ReadAll(logPath)
	if err != nil {
		t.Fatalf("orchevents.ReadAll: %v", err)
	}

	var sawChild, sawRunDone bool
	for _, ev := range events {
		switch ev.Type {
		case orchevents.ChildCompleted:
			sawChild = true
			if ev.Meta == nil {
				t.Fatalf("child_completed missing Meta: %+v", ev)
			}
			if _, ok := ev.Meta["slot_guard_admitted"]; !ok {
				t.Errorf("child_completed Meta missing slot_guard_admitted: %+v", ev.Meta)
			}
			if _, ok := ev.Meta["cached_tokens"]; !ok {
				t.Errorf("child_completed Meta missing cached_tokens: %+v", ev.Meta)
			}
		case orchevents.RunCompleted:
			sawRunDone = true
			if ev.Meta == nil {
				t.Fatalf("run_completed missing Meta: %+v", ev)
			}
			for _, key := range []string{"slot_guard_admitted", "cached_tokens", "cache_n", "prompt_tps", "predicted_tps"} {
				if _, ok := ev.Meta[key]; !ok {
					t.Errorf("run_completed Meta missing %q: %+v", key, ev.Meta)
				}
			}
		}
	}
	if !sawChild || !sawRunDone {
		t.Logf("events raw:\n%s", raw)
		t.Fatalf("missing expected events: sawChild=%t, sawRunDone=%t", sawChild, sawRunDone)
	}
}
