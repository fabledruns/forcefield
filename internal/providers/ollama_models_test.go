package providers

import (
	"context"
	"net/http"
	"testing"
)

func TestOllamaListModels(t *testing.T) {
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want /api/tags", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[
			{"name":"ornith:9b","details":{"parameter_size":"9B","quantization_level":"Q4_K_M"}},
			{"name":"llama3.3","details":{}}
		]}`))
	})

	p := NewOllamaProvider(server, "ornith:9b")
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v, want two", models)
	}
	if models[0].ID != "ornith:9b" || models[0].Description == "" {
		t.Errorf("models[0] = %+v, want ID plus parameter description", models[0])
	}
	if models[1].ID != "llama3.3" || models[1].Description != "" {
		t.Errorf("models[1] = %+v, want llama3.3 without description", models[1])
	}
}

func TestOllamaCapabilities(t *testing.T) {
	caps := CapabilitiesFor("ollama")
	if !caps.Streaming || !caps.ToolCalling || !caps.Reasoning {
		t.Errorf("ollama capabilities = %+v, want streaming/tools/reasoning", caps)
	}
	if caps.Vision {
		t.Error("vision must stay false: no Forcefield message can carry images yet")
	}
}
