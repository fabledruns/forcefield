package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type mockLemonadeModel struct {
	ID               string
	Recipe           string
	Downloaded       bool
	Labels           []string
	ContextLength    int64
	MaxContextWindow int64
	ResolvedCtxSize  int
	Loaded           bool
	Device           string
	Pinned           bool
	IsBusy           bool
	ParallelSlots    int
}

type mockLemonadeServer struct {
	server *httptest.Server

	mu              sync.Mutex
	models          map[string]*mockLemonadeModel
	forceChatStatus int
	forceChatBody   string
	dropDoneMarker  bool
	requireAuth     string
	lastChatReq     map[string]any
	chatCount       int
	optionsHits     int
	healthHits      int
	modelsHits      int
}

func newMockLemonadeServer() *mockLemonadeServer {
	m := &mockLemonadeServer{
		models: map[string]*mockLemonadeModel{
			"Qwen2.5-Coder-3B-Instruct-GGUF": {
				ID:               "Qwen2.5-Coder-3B-Instruct-GGUF",
				Recipe:           "llamacpp",
				Downloaded:       true,
				Labels:           []string{"chat", "coding", "tool-calling", "reasoning"},
				ContextLength:    32768,
				MaxContextWindow: 32768,
				ResolvedCtxSize:  8192,
				Loaded:           true,
				Device:           "gpu",
				ParallelSlots:    1,
			},
			"Unpulled-7B-GGUF": {
				ID:               "Unpulled-7B-GGUF",
				Recipe:           "llamacpp",
				Downloaded:       false,
				Labels:           []string{"chat"},
				MaxContextWindow: 32768,
				ResolvedCtxSize:  8192,
			},
		},
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	return m
}

func (m *mockLemonadeServer) close() {
	m.server.Close()
}

func (m *mockLemonadeServer) url() string {
	return m.server.URL
}

func stripQuadPrefix(path string) (string, bool) {
	for _, prefix := range []string{"/api/v1", "/api/v0", "/v1", "/v0"} {
		if strings.HasPrefix(path, prefix+"/") || path == prefix {
			rest := strings.TrimPrefix(path, prefix)
			if rest == "" {
				rest = "/"
			}
			return rest, true
		}
	}
	return path, false
}

func (m *mockLemonadeServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	reqAuth := m.requireAuth
	m.mu.Unlock()
	if reqAuth != "" && r.Header.Get("Authorization") != "Bearer "+reqAuth {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
		return
	}

	if r.URL.Path == "/live" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
		return
	}

	subpath, isQuad := stripQuadPrefix(r.URL.Path)
	if !isQuad {
		http.NotFound(w, r)
		return
	}

	switch {
	case subpath == "/health" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		m.handleHealth(w, r)
	case subpath == "/models" && r.Method == http.MethodGet:
		m.handleModels(w, r)
	case strings.HasPrefix(subpath, "/models/") && strings.HasSuffix(subpath, "/options") && r.Method == http.MethodGet:
		id := strings.TrimSuffix(strings.TrimPrefix(subpath, "/models/"), "/options")
		m.handleModelOptions(w, id)
	case strings.HasPrefix(subpath, "/models/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(subpath, "/models/")
		m.handleModelByID(w, id)
	case subpath == "/chat/completions" && r.Method == http.MethodPost:
		m.handleChatCompletions(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *mockLemonadeServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthHits++

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	loaded := make([]map[string]any, 0)
	pinnedCount := 0
	for _, mdl := range m.models {
		if !mdl.Loaded {
			continue
		}
		if mdl.Pinned {
			pinnedCount++
		}
		np := mdl.ParallelSlots
		if np <= 0 {
			np = 1
		}
		loaded = append(loaded, map[string]any{
			"model_name":         mdl.ID,
			"checkpoint":         mdl.ID,
			"type":               "llm",
			"device":             mdl.Device,
			"pinned":             mdl.Pinned,
			"is_busy":            mdl.IsBusy,
			"is_streaming":       mdl.IsBusy,
			"status":             "ready",
			"recipe":             mdl.Recipe,
			"max_context_window": mdl.MaxContextWindow,
			"launch_command":     []string{"llama-server", "--parallel", fmt.Sprintf("%d", np)},
			"recipe_options":     map[string]any{"ctx_size": mdl.ResolvedCtxSize},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":            "ok",
		"all_models_loaded": loaded,
		"pinned_models":     map[string]int{"llm": pinnedCount},
		"max_models":        map[string]int{"llm": 1},
	})
}

func (m *mockLemonadeServer) handleModels(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelsHits++

	showAll := r.URL.Query().Get("show_all") == "true"
	items := make([]map[string]any, 0, len(m.models))
	for _, mdl := range m.models {
		if !showAll && !mdl.Downloaded {
			continue
		}
		items = append(items, map[string]any{
			"id":                 mdl.ID,
			"checkpoint":         mdl.ID,
			"recipe":             mdl.Recipe,
			"downloaded":         mdl.Downloaded,
			"labels":             mdl.Labels,
			"context_length":     mdl.ContextLength,
			"max_context_window": mdl.MaxContextWindow,
			"max_output_tokens":  4096,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
}

func (m *mockLemonadeServer) handleModelOptions(w http.ResponseWriter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.optionsHits++

	mdl, ok := m.models[id]
	if !ok {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"defaults":          map[string]any{"ctx_size": -1},
		"saved":             map[string]any{},
		"effective":         map[string]any{"ctx_size": mdl.ResolvedCtxSize},
		"resolved_ctx_size": mdl.ResolvedCtxSize,
	})
}

func (m *mockLemonadeServer) handleModelByID(w http.ResponseWriter, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mdl, ok := m.models[id]
	if !ok {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":                 mdl.ID,
		"context_length":     mdl.ContextLength,
		"max_context_window": mdl.MaxContextWindow,
		"max_output_tokens":  4096,
	})
}

func (m *mockLemonadeServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	bodyBytes, _ := io.ReadAll(r.Body)
	var reqMap map[string]any
	_ = json.Unmarshal(bodyBytes, &reqMap)

	m.mu.Lock()
	m.chatCount++
	m.lastChatReq = reqMap
	forceStatus := m.forceChatStatus
	forceBody := m.forceChatBody
	dropDone := m.dropDoneMarker
	m.mu.Unlock()

	if forceStatus != 0 {
		w.WriteHeader(forceStatus)
		fmt.Fprint(w, forceBody)
		return
	}

	stream, _ := reqMap["stream"].(bool)
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"choices":[{"message":{"content":"pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":8}},
			"timings":{"cache_n":8,"prompt_n":4,"prompt_ms":14.5,"prompt_per_second":275.8,"predicted_n":3,"predicted_ms":42.0,"predicted_per_second":71.4}
		}`)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	writeEvent := func(payload string) {
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}

	// 1. Optional thinking delta when enable_thinking is true.
	if enableThinking, _ := reqMap["enable_thinking"].(bool); enableThinking {
		writeEvent(`{"choices":[{"delta":{"reasoning_content":"checking prefix"}}]}`)
	}

	// 2. Content + tool call fragment + final content + finish_reason chunk (WITHOUT usage yet).
	writeEvent(`{"choices":[{"delta":{"content":"hello "}}]}`)
	writeEvent(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`)
	writeEvent(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"README.md\"}"}}]}}]}`)
	writeEvent(`{"choices":[{"delta":{"content":"world"},"finish_reason":"tool_calls"}]}`)

	// 3. Lemonade trailing chunk AFTER finish_reason with empty choices carrying usage + timings!
	writeEvent(`{"choices":[],"usage":{"prompt_tokens":42,"completion_tokens":9,"total_tokens":51,"prompt_tokens_details":{"cached_tokens":30}},"timings":{"cache_n":30,"prompt_n":12,"prompt_ms":25.0,"prompt_per_second":480.0,"predicted_n":9,"predicted_ms":100.0,"predicted_per_second":90.0}}`)

	if !dropDone {
		writeEvent("[DONE]")
	}
}
