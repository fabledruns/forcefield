package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// newTestHTTPServer starts a server for the lifetime of the test and
// returns its base URL.
func newTestHTTPServer(t *testing.T, handle http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handle)
	t.Cleanup(server.Close)
	return server.URL
}

// newCatalogRuntime builds a Runtime wired to a synthetic config and an
// isolated discovery service over an empty factory registry; tests
// register exactly the transports they need via extraFactories.
func newCatalogRuntime(t *testing.T, cfg *config.Config, extraFactories ...func(*providers.FactoryRegistry)) *Runtime {
	t.Helper()
	isolateRuntimeHome(t)

	factories := providers.NewFactoryRegistry()
	factories.MustRegister("ollama", func(spec providers.Spec) (providers.ModelProvider, error) {
		return providers.NewOllamaProvider(spec.BaseURL, spec.Model), nil
	})
	for _, register := range extraFactories {
		register(factories)
	}

	manager := tools.NewManager(tools.NewRegistry())
	return &Runtime{
		cfg:       cfg,
		agent:     agent.New("test", "system", ""),
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		discovery: providers.NewDiscovery(factories),
	}
}

// ocFactory registers the shared OpenAI-compatible transport backed by
// real adapters.
func ocFactory(reg *providers.FactoryRegistry) {
	reg.MustRegister("openai-compatible", func(spec providers.Spec) (providers.ModelProvider, error) {
		return providers.NewOpenAICompatible(spec), nil
	})
}

func catalogConfig(serverURL string) *config.Config {
	return &config.Config{
		Model: config.Model{Provider: "lab", Name: "custom-model"},
		Providers: map[string]config.ProviderConfig{
			"lab": {Type: "openai-compatible", BaseURL: serverURL, Models: []string{"zeta", "alpha"}},
		},
	}
}

func TestModelCatalogFallsBackToConfiguredModelsBeforeDiscovery(t *testing.T) {
	var count atomic.Int64
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"discovered"}]}`)
	})

	rt := newCatalogRuntime(t, catalogConfig(server), ocFactory)

	models, state := rt.ModelCatalog("lab")
	if state != ModelsStale {
		t.Fatalf("state = %v, want ModelsStale before discovery", state)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	want := "custom-model,alpha,zeta"
	if strings.Join(ids, ",") != want {
		t.Fatalf("order = %v, want %s (active model first, fallbacks sorted)", ids, want)
	}
	if n := count.Load(); n != 0 {
		t.Fatalf("requests = %d, ModelCatalog must never touch the network", n)
	}
}

func TestDiscoverModelsPopulatesFreshCacheWithDeterministicOrder(t *testing.T) {
	var count atomic.Int64
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"m2"},{"id":"m1"},{"id":"m3"}]}`)
	})

	rt := newCatalogRuntime(t, catalogConfig(server), ocFactory)

	models, err := rt.DiscoverModels(context.Background(), "lab")
	if err != nil {
		t.Fatalf("DiscoverModels() error = %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %+v, want three discovered entries", models)
	}

	got, state := rt.ModelCatalog("lab")
	if state != ModelsFresh {
		t.Fatalf("state = %v, want ModelsFresh after successful discovery", state)
	}
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	// Active model leads; discovered models follow in sorted order.
	if strings.Join(ids, ",") != "custom-model,m1,m2,m3" {
		t.Fatalf("order = %v, want active first then discovered sorted", ids)
	}
	if n := count.Load(); n != 1 {
		t.Fatalf("requests = %d, want exactly one fetch", n)
	}

	// A second catalog read is a pure cache hit.
	rt.ModelCatalog("lab")
	if n := count.Load(); n != 1 {
		t.Fatalf("requests after re-read = %d, cache must serve fresh listings", n)
	}
}

func TestConfiguredModelSurvivesEvenIfAbsentFromDiscovery(t *testing.T) {
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"only-listed"}]}`)
	})

	rt := newCatalogRuntime(t, catalogConfig(server), ocFactory)
	if _, err := rt.DiscoverModels(context.Background(), "lab"); err != nil {
		t.Fatalf("DiscoverModels() error = %v", err)
	}

	models, _ := rt.ModelCatalog("lab")
	found := false
	for _, m := range models {
		if m.ID == "custom-model" {
			found = true
		}
	}
	if !found || models[0].ID != "custom-model" {
		t.Fatalf("models = %+v, want the configured custom-model kept and first", models)
	}
}

func TestMissingAPIKeyFailsDiscoveryWithGuidance(t *testing.T) {
	cfg := &config.Config{
		Model: config.Model{Provider: "openai", Name: "gpt-4o-mini"},
	}
	rt := newCatalogRuntime(t, cfg, ocFactory)
	t.Setenv("OPENAI_API_KEY", "")

	_, err := rt.DiscoverModels(context.Background(), "openai")
	if err == nil {
		t.Fatal("DiscoverModels() succeeded without any API key")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("error = %q, want guidance naming OPENAI_API_KEY", err)
	}

	// The provider stays perfectly usable as far as the catalog cares:
	// fallback models are still offered.
	models, _ := rt.ModelCatalog("openai")
	if len(models) == 0 {
		t.Fatal("catalog empty despite catalog defaults existing")
	}
}

func TestUnsupportedDiscoveryStateForTransportWithoutListing(t *testing.T) {
	cfg := &config.Config{
		Model: config.Model{Provider: "plain", Name: "m"},
		Providers: map[string]config.ProviderConfig{
			"plain": {Type: "openai-compatible", BaseURL: "http://localhost:9"},
		},
	}
	rt := newCatalogRuntime(t, cfg, func(reg *providers.FactoryRegistry) {
		// Same protocol name as config, but this test's registry builds an
		// adapter that cannot enumerate models - e.g. a stripped-down
		// transport. Resolution still accepts the type; discovery reports
		// it unsupported instead of failing.
		reg.MustRegister("openai-compatible", func(providers.Spec) (providers.ModelProvider, error) {
			return stubRuntimeProvider{}, nil
		})
	})

	models, state := rt.ModelCatalog("plain")
	if state != ModelsUnsupported {
		t.Fatalf("state = %v, want ModelsUnsupported", state)
	}
	if len(models) == 0 {
		t.Fatal("static models missing for unsupported transport")
	}

	_, err := rt.DiscoverModels(context.Background(), "plain")
	if !errors.Is(err, providers.ErrNoDiscovery) {
		t.Fatalf("error = %v, want ErrNoDiscovery", err)
	}
}

type stubRuntimeProvider struct{}

func (stubRuntimeProvider) StreamChat(_ context.Context, _ []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	return nil, nil
}

func TestProviderSwitchWhileDiscoveryInFlight(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })

	var labRequests, otherRequests atomic.Int64
	labServer := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		labRequests.Add(1)
		<-release // hold lab's discovery open across the provider switch
		fmt.Fprint(w, `{"data":[{"id":"lab-model"}]}`)
	})
	otherServer := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		otherRequests.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"other-model"}]}`)
	})

	cfg := &config.Config{
		Model: config.Model{Provider: "lab", Name: "custom-model"},
		Providers: map[string]config.ProviderConfig{
			"lab":   {Type: "openai-compatible", BaseURL: labServer},
			"other": {Type: "openai-compatible", BaseURL: otherServer, Model: "other-default"},
		},
	}
	rt := newCatalogRuntime(t, cfg, ocFactory)

	done := make(chan error, 1)
	go func() {
		_, err := rt.DiscoverModels(context.Background(), "lab")
		done <- err
	}()

	time.Sleep(30 * time.Millisecond) // let lab's request reach the server

	// Switch away while lab's listing is still in flight.
	rt.cfg.Model.Provider = "other"

	models, state := rt.ModelCatalog("other")
	if state == ModelsFresh {
		t.Fatal("other reported fresh before any fetch completed")
	}
	// The active model (carried over by the switch) leads, then the
	// entry's configured default.
	if len(models) != 2 || models[0].ID != "custom-model" || models[1].ID != "other-default" {
		t.Fatalf("other's catalog = %+v, want carried-over active model then entry default", models)
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("in-flight lab discovery errored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lab discovery never finished")
	}

	// Lab's late result is cached under its own key and does not leak
	// into the now-active other provider.
	labModels, _ := rt.ModelCatalog("lab")
	if len(labModels) == 0 || labModels[len(labModels)-1].ID != "lab-model" {
		t.Fatalf("lab catalog after switch = %+v, want its discovered model cached", labModels)
	}
	if n := otherRequests.Load(); n != 0 {
		t.Fatalf("other requests = %d, switching must not trigger fetches", n)
	}
}
