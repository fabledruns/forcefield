package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/tools"
)

// discOCServer spins up an OpenAI-compatible server whose /models
// responses are fully controlled by the test, counting every request.
func discOCServer(t *testing.T, handle func(t *testing.T, w http.ResponseWriter, r *http.Request), count *atomic.Int64) string {
	t.Helper()
	return newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		handle(t, w, r)
	})
}

func discSpec(url, apiKey string) Spec {
	return Spec{ID: "lab", Type: "openai-compatible", Label: "Lab", BaseURL: url, Model: "m", APIKey: apiKey}
}

func TestDiscoveryFetchMapsSortsAndCaches(t *testing.T) {
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-lab" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"zulu"},{"id":"alpha"},{"id":"mike"}]}`)
	}, &count)

	d := NewDiscovery(DefaultFactories())
	spec := discSpec(url, "sk-lab")

	models, err := d.Fetch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
		if m.Provider != "lab" || m.Name != m.ID {
			t.Errorf("model = %+v, want provider/name filled from ID", m)
		}
		if m.ContextWindow != 0 {
			t.Errorf("context window = %d, want 0 (endpoint reports none)", m.ContextWindow)
		}
	}
	if strings.Join(ids, ",") != "alpha,mike,zulu" {
		t.Errorf("ids = %v, want deterministic sorted order", ids)
	}

	// Fresh cache serves without network.
	cached, ok := d.Cached(spec)
	if !ok || len(cached) != 3 {
		t.Fatalf("Cached() = %v %v, want 3 fresh models", cached, ok)
	}
	if n := count.Load(); n != 1 {
		t.Fatalf("requests = %d, want 1 (second read must be a cache hit)", n)
	}

	// Explicit refresh bypasses the cache.
	if _, err := d.Fetch(context.Background(), spec); err != nil {
		t.Fatalf("refresh Fetch() error = %v", err)
	}
	if n := count.Load(); n != 2 {
		t.Fatalf("requests after refresh = %d, want 2", n)
	}
}

func TestDiscoveryEmptyListIsSuccess(t *testing.T) {
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}, &count)

	d := NewDiscovery(DefaultFactories())
	models, err := d.Fetch(context.Background(), discSpec(url, ""))
	if err != nil {
		t.Fatalf("Fetch() error = %v, want success with empty list", err)
	}
	if len(models) != 0 {
		t.Fatalf("models = %+v, want empty", models)
	}
}

func TestDiscoveryMalformedResponseIsProtocolError(t *testing.T) {
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "{truncated")
	}, &count)

	d := NewDiscovery(DefaultFactories())
	_, err := d.Fetch(context.Background(), discSpec(url, ""))
	if err == nil {
		t.Fatal("Fetch() succeeded on malformed body")
	}
	if got := Classify(err); got != ErrKindProtocol {
		t.Errorf("kind = %v, want ErrKindProtocol", got)
	}
}

func TestDiscoveryErrorStatusesAreClassified(t *testing.T) {
	statuses := map[int]ErrorKind{
		http.StatusUnauthorized:        ErrKindAuth,
		http.StatusForbidden:           ErrKindAuth,
		http.StatusNotFound:            ErrKindNotFound,
		http.StatusTooManyRequests:     ErrKindRateLimit,
		http.StatusInternalServerError: ErrKindServer,
	}
	for status, want := range statuses {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			var count atomic.Int64
			url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"boom"}}`)
			}, &count)

			d := NewDiscovery(DefaultFactories())
			d.SetTTL(time.Nanosecond) // never serve stale entries across subtests
			_, err := d.Fetch(context.Background(), discSpec(url, ""))
			if err == nil {
				t.Fatal("Fetch() succeeded, want status error")
			}
			if got := Classify(err); got != want {
				t.Errorf("kind = %v, want %v", got, want)
			}
		})
	}
}

func TestDiscoveryDeadlineIsTimeout(t *testing.T) {
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, `{"data":[]}`)
	}, &count)

	d := NewDiscovery(DefaultFactories())
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := d.Fetch(ctx, discSpec(url, ""))
	if err == nil {
		t.Fatal("Fetch() succeeded against a hanging endpoint")
	}
	if got := Classify(err); got != ErrKindTimeout {
		t.Errorf("kind = %v (%v), want ErrKindTimeout", got, err)
	}
}

func TestDiscoveryCancellationStopsRequest(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		<-release
		fmt.Fprint(w, `{"data":[]}`)
	}, &count)

	d := NewDiscovery(DefaultFactories())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := d.Fetch(ctx, discSpec(url, ""))
		done <- err
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fetch did not return after cancellation")
	}
}

func TestDiscoveryUnsupportedTransport(t *testing.T) {
	reg := NewFactoryRegistry()
	reg.MustRegister("nodisc", func(Spec) (ModelProvider, error) { return stubProvider{}, nil })

	d := NewDiscovery(reg)
	spec := Spec{ID: "s", Type: "nodisc", BaseURL: "http://localhost:1", Model: "m"}

	if d.Supports(spec) {
		t.Error("Supports() = true for a transport without ListModels")
	}
	_, err := d.Fetch(context.Background(), spec)
	if !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("error = %v, want ErrNoDiscovery", err)
	}
}

type stubProvider struct{}

func (stubProvider) StreamChat(_ context.Context, _ []Message, _ []tools.Definition) (<-chan StreamEvent, error) {
	return nil, nil
}

func TestDiscoveryCacheExpiry(t *testing.T) {
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
	}, &count)

	d := NewDiscovery(DefaultFactories())
	now := time.Unix(0, 0)
	d.now = func() time.Time { return now }
	spec := discSpec(url, "")

	if _, err := d.Fetch(context.Background(), spec); err != nil {
		t.Fatalf("first Fetch() error = %v", err)
	}
	now = now.Add(d.ttl - time.Minute)
	if _, ok := d.Cached(spec); !ok {
		t.Fatal("entry expired before its TTL")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := d.Cached(spec); ok {
		t.Fatal("stale entry served after TTL")
	}
	if n := count.Load(); n != 1 {
		t.Fatalf("requests = %d, expiry alone must not trigger fetches", n)
	}
}

func TestDiscoveryFailedRefreshPreservesCachedModels(t *testing.T) {
	var fail atomic.Bool
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"exploded"}}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"keeper"},{"id":"other"}]}`)
	}, &count)

	d := NewDiscovery(DefaultFactories())
	spec := discSpec(url, "")

	if _, err := d.Fetch(context.Background(), spec); err != nil {
		t.Fatalf("initial Fetch() error = %v", err)
	}
	fail.Store(true)
	if _, err := d.Fetch(context.Background(), spec); err == nil {
		t.Fatal("refresh unexpectedly succeeded")
	}

	cached, ok := d.Cached(spec)
	if !ok || len(cached) != 2 || cached[0].ID != "keeper" {
		t.Fatalf("cached = %v %v, want previous listing retained", cached, ok)
	}
}

func TestDiscoveryCacheKeySeparatesCredentialsNotExposeThem(t *testing.T) {
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"shared"}]}`)
	}, &count)

	d := NewDiscovery(DefaultFactories())

	accountA := discSpec(url, "sk-account-a")
	accountB := discSpec(url, "sk-account-b")

	if _, err := d.Fetch(context.Background(), accountA); err != nil {
		t.Fatalf("Fetch(A) error = %v", err)
	}
	cachedA, freshA := d.Cached(accountA)
	if !freshA || len(cachedA) != 1 {
		t.Fatalf("Cached(A) = %v %v", cachedA, freshA)
	}
	// A different credential on the same endpoint must be its own entry...
	if _, fresh := d.Cached(accountB); fresh {
		t.Fatal("account B served account A's cached listing")
	}
	// ...and neither may surface the raw key material.
	if strings.Contains(string(discoveryKeyFor(accountA)), "sk-account-a") {
		t.Fatal("raw API key appears in the cache key")
	}

	if _, err := d.Fetch(context.Background(), accountB); err != nil {
		t.Fatalf("Fetch(B) error = %v", err)
	}
	if n := count.Load(); n != 2 {
		t.Fatalf("requests = %d, want separate fetch per credential", n)
	}
}

func TestDiscoveryRedactsAPIKeysFromErrors(t *testing.T) {
	const key = "sk-secret-value"
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"bad key %s"}}`, key)
	}, &count)

	d := NewDiscovery(DefaultFactories())
	_, err := d.Fetch(context.Background(), discSpec(url, key))
	if err == nil {
		t.Fatal("Fetch() succeeded, want auth error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error leaked the key: %q", err.Error())
	}
}

func TestDiscoveryConcurrentFetchesShareOneRequest(t *testing.T) {
	var count atomic.Int64
	url := discOCServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		time.Sleep(120 * time.Millisecond) // widen the race window
		fmt.Fprint(w, `{"data":[{"id":"only"}]}`)
	}, &count)

	d := NewDiscovery(DefaultFactories())
	spec := discSpec(url, "")

	const callers = 8
	var wg sync.WaitGroup
	results := make([][]Model, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			models, err := d.Fetch(context.Background(), spec)
			results[i], errs[i] = models, err
		}(i)
	}
	wg.Wait()

	for i := 0; i < callers; i++ {
		if err := errs[i]; err != nil {
			t.Fatalf("caller %d error = %v", i, err)
		}
		if len(results[i]) != 1 || results[i][0].ID != "only" {
			t.Fatalf("caller %d saw models = %+v, want the shared result", i, results[i])
		}
	}
	if n := count.Load(); n > 2 {
		t.Fatalf("server received %d requests, want single-flight (at most 2)", n)
	}
}

func TestDiscoveryOllamaViaService(t *testing.T) {
	var count atomic.Int64
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want /api/tags", r.URL.Path)
		}
		fmt.Fprint(w, `{"models":[{"name":"ornith:9b","details":{"parameter_size":"9B"}},{"name":"llama3.3"}]}`)
	})

	d := NewDiscovery(DefaultFactories())
	models, err := d.Fetch(context.Background(), Spec{
		ID: "ollama", Type: "ollama", BaseURL: server, Model: "ornith:9b",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(models) != 2 || models[0].ID != "llama3.3" || models[1].ID != "ornith:9b" {
		t.Fatalf("models = %+v, want sorted ollama listings", models)
	}
}
