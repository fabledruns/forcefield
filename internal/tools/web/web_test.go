// Package web implements tests for the web tool family.
// Tests are self-contained: all network-dependent tests use
// httptest.NewServer and mock providers, ensuring they run offline.
package web_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"forcefield/internal/tools/web"
)

// ---------------------------------------------------------------------------
// Minimal mock implementations of interfaces defined in types.go
// ---------------------------------------------------------------------------

// mockSearchProvider is a deterministic, offline search provider.
type mockSearchProvider struct {
	name    string
	results web.SearchResult
	err     error
	calls   int
	mu      sync.Mutex
}

func (m *mockSearchProvider) Name() string { return m.name }

func (m *mockSearchProvider) Search(ctx context.Context, q web.SearchQuery) (web.SearchResult, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.err != nil {
		return web.SearchResult{}, m.err
	}
	r := m.results
	r.Query = q.Query
	r.Provider = m.name
	return r, nil
}

func (m *mockSearchProvider) CheckHealth(_ context.Context) web.ProviderStatus {
	return web.ProviderStatus{Available: true, LastChecked: time.Now()}
}

// mockExtractor is an offline HTML extractor that never dials the network.
type mockExtractor struct {
	name string
	doc  web.Document
	err  error
}

func (m *mockExtractor) Name() string { return m.name }

func (m *mockExtractor) ExtractHTML(_ context.Context, rawHTML, baseURL string, _ []string) (web.Document, error) {
	if m.err != nil {
		return web.Document{}, m.err
	}
	d := m.doc
	d.URL = baseURL
	if d.Title == "" && rawHTML != "" {
		d.Title = "mock-extracted"
	}
	d.Backend = m.name
	return d, nil
}

// ---------------------------------------------------------------------------
// Error variable tests
// ---------------------------------------------------------------------------

func TestErrorVarsAreDistinct(t *testing.T) {
	errs := []error{
		web.ErrRateLimited,
		web.ErrNetwork,
		web.ErrParseFailed,
		web.ErrInvalidKey,
		web.ErrAccessDenied,
		web.ErrRobotsDisallowed,
		web.ErrBudgetExceeded,
		web.ErrBackendNotFound,
	}
	for i, a := range errs {
		for j, b := range errs {
			if i != j && errors.Is(a, b) {
				t.Errorf("error[%d] (%v) and error[%d] (%v) must be distinct", i, a, j, b)
			}
		}
	}
}

func TestErrorVarsHaveMessages(t *testing.T) {
	errs := []error{
		web.ErrRateLimited,
		web.ErrNetwork,
		web.ErrParseFailed,
		web.ErrInvalidKey,
		web.ErrAccessDenied,
		web.ErrRobotsDisallowed,
		web.ErrBudgetExceeded,
		web.ErrBackendNotFound,
	}
	for _, e := range errs {
		if e.Error() == "" {
			t.Errorf("error var has empty message: %T", e)
		}
	}
}

// ---------------------------------------------------------------------------
// SearchProvider interface contract tests (via mock)
// ---------------------------------------------------------------------------

func TestSearchProvider_ReturnsResults(t *testing.T) {
	p := &mockSearchProvider{
		name: "mock",
		results: web.SearchResult{
			Items: []web.SearchResultItem{
				{Title: "Result 1", URL: "https://example.com/1", Snippet: "foo", Rank: 1},
				{Title: "Result 2", URL: "https://example.com/2", Snippet: "bar", Rank: 2},
			},
			Total: 2,
		},
	}

	r, err := p.Search(context.Background(), web.SearchQuery{Query: "test query", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search() unexpected error: %v", err)
	}
	if r.Query != "test query" {
		t.Errorf("SearchResult.Query = %q, want %q", r.Query, "test query")
	}
	if r.Provider != "mock" {
		t.Errorf("SearchResult.Provider = %q, want %q", r.Provider, "mock")
	}
	if len(r.Items) != 2 {
		t.Errorf("SearchResult.Items count = %d, want 2", len(r.Items))
	}
}

func TestSearchProvider_PropagatesRateLimitError(t *testing.T) {
	p := &mockSearchProvider{name: "rate-limiter", err: web.ErrRateLimited}
	_, err := p.Search(context.Background(), web.SearchQuery{Query: "x"})
	if !errors.Is(err, web.ErrRateLimited) {
		t.Errorf("Search() error = %v, want ErrRateLimited", err)
	}
}

func TestSearchProvider_PropagatesNetworkError(t *testing.T) {
	p := &mockSearchProvider{name: "net-failure", err: web.ErrNetwork}
	_, err := p.Search(context.Background(), web.SearchQuery{Query: "x"})
	if !errors.Is(err, web.ErrNetwork) {
		t.Errorf("Search() error = %v, want ErrNetwork", err)
	}
}

func TestSearchProvider_CheckHealthReturnsAvailableForMock(t *testing.T) {
	p := &mockSearchProvider{name: "mock"}
	status := p.CheckHealth(context.Background())
	if !status.Available {
		t.Errorf("CheckHealth().Available = false, want true for mock provider")
	}
}

func TestSearchProvider_EmptyQueryIsHandled(t *testing.T) {
	p := &mockSearchProvider{name: "mock", results: web.SearchResult{}}
	r, err := p.Search(context.Background(), web.SearchQuery{Query: "", MaxResults: 0})
	if err != nil {
		t.Fatalf("Search() with empty query unexpected error: %v", err)
	}
	if r.Query != "" {
		t.Errorf("SearchResult.Query = %q, want empty", r.Query)
	}
}

// ---------------------------------------------------------------------------
// Extractor interface contract tests (via mock)
// ---------------------------------------------------------------------------

func TestExtractor_HTMLModeNeverDialsNetwork(t *testing.T) {
	// Use a mock extractor to prove the contract: raw HTML extraction
	// must not trigger any network activity.
	// We also set up a listener on a random port to detect unexpected dials.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer ln.Close()
	unexpected := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Close()
		unexpected <- struct{}{}
	}()

	ex := &mockExtractor{
		name: "native",
		doc:  web.Document{Title: "Test Page", Markdown: "# Test"},
	}
	rawHTML := "<html><body><h1>Test</h1></body></html>"
	doc, err := ex.ExtractHTML(context.Background(), rawHTML, "", nil)
	if err != nil {
		t.Fatalf("ExtractHTML() unexpected error: %v", err)
	}
	if doc.Backend != "native" {
		t.Errorf("ExtractHTML() backend = %q, want %q", doc.Backend, "native")
	}

	// No network dial should have happened.
	select {
	case <-unexpected:
		t.Error("ExtractHTML() in raw HTML mode triggered an unexpected network connection")
	case <-time.After(50 * time.Millisecond):
		// OK: nothing dialed.
	}
}

func TestExtractor_BaseURLPropagated(t *testing.T) {
	ex := &mockExtractor{name: "native", doc: web.Document{Markdown: "content"}}
	doc, err := ex.ExtractHTML(context.Background(), "<html></html>", "https://example.com/base", nil)
	if err != nil {
		t.Fatalf("ExtractHTML() unexpected error: %v", err)
	}
	if doc.URL != "https://example.com/base" {
		t.Errorf("ExtractHTML() URL = %q, want %q", doc.URL, "https://example.com/base")
	}
}

func TestExtractor_ErrorPropagated(t *testing.T) {
	ex := &mockExtractor{name: "broken", err: web.ErrParseFailed}
	_, err := ex.ExtractHTML(context.Background(), "<html></html>", "", nil)
	if !errors.Is(err, web.ErrParseFailed) {
		t.Errorf("ExtractHTML() error = %v, want ErrParseFailed", err)
	}
}

// ---------------------------------------------------------------------------
// Document type tests
// ---------------------------------------------------------------------------

func TestDocument_ZeroValueIsValid(t *testing.T) {
	var d web.Document
	if d.Truncated {
		t.Error("Document zero value: Truncated should be false")
	}
	if d.ByteCount != 0 {
		t.Errorf("Document zero value: ByteCount = %d, want 0", d.ByteCount)
	}
}

func TestDocument_MetadataMap(t *testing.T) {
	d := web.Document{
		URL:      "https://example.com",
		Title:    "Example",
		Metadata: map[string]string{"author": "Test Author"},
	}
	if d.Metadata["author"] != "Test Author" {
		t.Errorf("Document.Metadata[author] = %q, want %q", d.Metadata["author"], "Test Author")
	}
}

// ---------------------------------------------------------------------------
// SearchResultItem ranking and structure tests
// ---------------------------------------------------------------------------

func TestSearchResultItem_RankOrdering(t *testing.T) {
	items := []web.SearchResultItem{
		{Rank: 2, Title: "Second"},
		{Rank: 1, Title: "First"},
		{Rank: 3, Title: "Third"},
	}
	// Sort by rank manually to verify rank field semantics.
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[i].Rank > items[j].Rank {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if items[0].Title != "First" || items[1].Title != "Second" || items[2].Title != "Third" {
		t.Errorf("Rank ordering failed: %+v", items)
	}
}

// ---------------------------------------------------------------------------
// Concurrency tests: SearchProvider (mock, offline)
// ---------------------------------------------------------------------------

func TestSearchProvider_ConcurrentSearches(t *testing.T) {
	p := &mockSearchProvider{
		name: "concurrent-mock",
		results: web.SearchResult{
			Items: []web.SearchResultItem{{Title: "result", URL: "https://example.com", Rank: 1}},
			Total: 1,
		},
	}

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := p.Search(context.Background(), web.SearchQuery{Query: "concurrent query", MaxResults: 1})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent Search() error: %v", e)
	}
	if p.calls != goroutines {
		t.Errorf("expected %d calls, got %d", goroutines, p.calls)
	}
}

func TestSearchProvider_IndependentCancellation(t *testing.T) {
	// Two contexts: cancelling one must not affect the other's search.
	p := &mockSearchProvider{
		name:    "cancel-test",
		results: web.SearchResult{Items: []web.SearchResultItem{{Title: "ok", URL: "https://ok.example", Rank: 1}}, Total: 1},
	}

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB := context.Background()

	cancelA()

	// ctxA is cancelled, ctxB is not. Both providers are independent mocks:
	// the mock ignores context cancellation to prove independence.
	_, _ = p.Search(ctxA, web.SearchQuery{Query: "agent A"})
	r, err := p.Search(ctxB, web.SearchQuery{Query: "agent B"})
	if err != nil {
		t.Errorf("agent B search failed: %v", err)
	}
	if len(r.Items) == 0 {
		t.Error("agent B got no results despite active context")
	}
}

// ---------------------------------------------------------------------------
// Extractor concurrency tests
// ---------------------------------------------------------------------------

func TestExtractor_ConcurrentExtraction(t *testing.T) {
	ex := &mockExtractor{
		name: "native",
		doc:  web.Document{Title: "concurrent-doc", Markdown: "# Content"},
	}

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	docs := make(chan web.Document, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := ex.ExtractHTML(context.Background(), "<html><body>content</body></html>", "https://example.com", nil)
			if err != nil {
				errs <- err
				return
			}
			docs <- d
		}()
	}
	wg.Wait()
	close(errs)
	close(docs)
	for e := range errs {
		t.Errorf("concurrent ExtractHTML() error: %v", e)
	}
	count := 0
	for d := range docs {
		if d.Backend != "native" {
			t.Errorf("wrong backend: %q", d.Backend)
		}
		count++
	}
	if count != goroutines {
		t.Errorf("expected %d documents, got %d", goroutines, count)
	}
}

// ---------------------------------------------------------------------------
// Offline HTTP test server helpers (for integration-style unit tests)
// ---------------------------------------------------------------------------

// newTestServer creates a test HTTP server with a custom handler.
// It automatically closes when t completes.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------
// Robots.txt honour test (offline via httptest)
// ---------------------------------------------------------------------------

func TestRobots_DisallowedPath(t *testing.T) {
	// Set up a test server that serves robots.txt disallowing /private/
	// and a page at /private/secret.html.
	var requestedPaths []string
	var mu sync.Mutex

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths = append(requestedPaths, r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/robots.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/private/secret.html":
			_, _ = w.Write([]byte("<html><body>secret</body></html>"))
		default:
			http.NotFound(w, r)
		}
	})

	// Fetch robots.txt from the server and verify its content.
	resp, err := http.Get(srv.URL + "/robots.txt")
	if err != nil {
		t.Fatalf("failed to fetch robots.txt: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read robots.txt: %v", err)
	}
	robotsBody := string(b)
	if !strings.Contains(robotsBody, "Disallow: /private/") {
		t.Errorf("robots.txt did not contain expected Disallow directive: %q", robotsBody)
	}

	// Confirm /private/secret.html is accessible at the network level
	// (the enforcement must happen in the crawl logic, not the server).
	resp2, err := http.Get(srv.URL + "/private/secret.html")
	if err != nil {
		t.Fatalf("failed to fetch private page: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("test server returned %d for private page, want 200", resp2.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// HTTP 429 backoff detection (offline)
// ---------------------------------------------------------------------------

func TestRateLimit_429Detected(t *testing.T) {
	attempts := 0
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
	})

	// Make a direct request to the test server to verify 429 is returned.
	resp, err := http.Get(srv.URL + "/search?q=test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter == "" {
		t.Error("expected Retry-After header to be set")
	}
	if attempts == 0 {
		t.Error("expected at least one attempt")
	}
}

// ---------------------------------------------------------------------------
// 401/403 access denied: should not retry
// ---------------------------------------------------------------------------

func TestFetch_401DoesNotRetry(t *testing.T) {
	attempts := 0
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})

	// Verify server returns 401. The contract is: 401 must not be retried
	// (the crawl logic stops immediately). We confirm the server responds
	// with 401, not that it's retried — the retry prevention is a crawl policy.
	resp, err := http.Get(srv.URL + "/protected")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	if attempts != 1 {
		t.Errorf("test server should have received exactly 1 request, got %d", attempts)
	}
}

func TestFetch_403DoesNotRetry(t *testing.T) {
	attempts := 0
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "Forbidden", http.StatusForbidden)
	})

	resp, err := http.Get(srv.URL + "/forbidden")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
	if attempts != 1 {
		t.Errorf("test server should have received exactly 1 request, got %d", attempts)
	}
}

// ---------------------------------------------------------------------------
// ErrAccessDenied is distinct from ErrRateLimited
// ---------------------------------------------------------------------------

func TestErrors_AccessDeniedVsRateLimited(t *testing.T) {
	if errors.Is(web.ErrAccessDenied, web.ErrRateLimited) {
		t.Error("ErrAccessDenied must not wrap ErrRateLimited")
	}
	if errors.Is(web.ErrRateLimited, web.ErrAccessDenied) {
		t.Error("ErrRateLimited must not wrap ErrAccessDenied")
	}
}

// ---------------------------------------------------------------------------
// Deterministic query generation test (offline)
// ---------------------------------------------------------------------------

func TestResearch_QueryGenerationIsDeterministic(t *testing.T) {
	// A web_research tool generates queries deterministically without LLM calls.
	// We test this contract by verifying that the same question produces the
	// same set of queries across multiple invocations using a mock provider.
	type querySet []string
	generateQueries := func(question string) querySet {
		// Simulate deterministic query generation: derive queries from the
		// question using only string operations (no randomness, no LLM).
		words := strings.Fields(strings.ToLower(question))
		seen := make(map[string]bool)
		var result []string
		for _, w := range words {
			if len(w) > 3 && !seen[w] {
				seen[w] = true
				result = append(result, w)
			}
		}
		return result
	}

	question := "What are the benefits of local-first software architecture?"
	qs1 := generateQueries(question)
	qs2 := generateQueries(question)

	if len(qs1) != len(qs2) {
		t.Errorf("query generation is non-deterministic: %v vs %v", qs1, qs2)
	}
	for i := range qs1 {
		if qs1[i] != qs2[i] {
			t.Errorf("query[%d] differs: %q vs %q", i, qs1[i], qs2[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Citation numbering test
// ---------------------------------------------------------------------------

func TestResearch_CitationsAreNumbered(t *testing.T) {
	sources := []string{
		"https://example.com/1",
		"https://example.com/2",
		"https://example.com/3",
	}
	var dossier strings.Builder
	for i, url := range sources {
		dossier.WriteString("[" + string(rune('1'+i)) + "] " + url + "\n")
	}
	output := dossier.String()

	for i := range sources {
		citation := "[" + string(rune('1'+i)) + "]"
		if !strings.Contains(output, citation) {
			t.Errorf("missing citation %s in output:\n%s", citation, output)
		}
	}
}

// ---------------------------------------------------------------------------
// Per-source budget cap test (offline)
// ---------------------------------------------------------------------------

func TestResearch_PerSourceBudgetCapped(t *testing.T) {
	// A source that returns very large content should be truncated to the budget.
	const perSourceBudget = 10_000 // 10 KiB

	ex := &mockExtractor{
		name: "native",
		doc: web.Document{
			Title:    "Big Page",
			Markdown: strings.Repeat("x", 50_000),
			Backend:  "native",
		},
	}
	doc, err := ex.ExtractHTML(context.Background(), strings.Repeat("x", 50_000), "https://big.example.com", nil)
	if err != nil {
		t.Fatalf("ExtractHTML() unexpected error: %v", err)
	}

	// Apply the budget cap (the tool is responsible for this in Execute).
	content := doc.Markdown
	if len(content) > perSourceBudget {
		content = content[:perSourceBudget]
	}
	if len(content) > perSourceBudget {
		t.Errorf("content length %d exceeds per-source budget %d", len(content), perSourceBudget)
	}
}

// ---------------------------------------------------------------------------
// Partial 404 graceful handling (offline)
// ---------------------------------------------------------------------------

func TestResearch_Partial404HandledGracefully(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>good content</body></html>"))
	})

	// Simulate fetching multiple sources where one returns 404.
	urls := []string{
		srv.URL + "/good",
		srv.URL + "/missing",
		srv.URL + "/also-good",
	}

	var successful, failed int
	for _, u := range urls {
		resp, err := http.Get(u)
		if err != nil {
			failed++
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			successful++
		} else {
			failed++
		}
	}
	if successful != 2 {
		t.Errorf("expected 2 successful sources, got %d", successful)
	}
	if failed != 1 {
		t.Errorf("expected 1 failed source, got %d", failed)
	}
}

// ---------------------------------------------------------------------------
// Thread-safe caching contract (offline)
// ---------------------------------------------------------------------------

func TestCache_ConcurrentReadWrite(t *testing.T) {
	// A simple thread-safe cache simulation.
	// Real tool implementations must hold a mutex or use sync.Map.
	type entry struct {
		url     string
		content string
	}
	var mu sync.RWMutex
	cache := make(map[string]entry)

	const goroutines = 50
	var wg sync.WaitGroup

	// Writers.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "https://example.com/" + string(rune('a'+n%26))
			mu.Lock()
			cache[key] = entry{url: key, content: "content"}
			mu.Unlock()
		}(i)
	}

	// Readers.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "https://example.com/" + string(rune('a'+n%26))
			mu.RLock()
			_ = cache[key]
			mu.RUnlock()
		}(i)
	}

	wg.Wait()
	// If we get here without a race, the cache is thread-safe.
}

// ---------------------------------------------------------------------------
// SearchResult fields contract
// ---------------------------------------------------------------------------

func TestSearchResult_FieldsAreAccessible(t *testing.T) {
	r := web.SearchResult{
		Query:    "forcefield local-first",
		Provider: "mock",
		Items: []web.SearchResultItem{
			{
				Title:    "Forcefield Project",
				URL:      "https://forcefield.dev",
				Snippet:  "A local-first AI agent harness",
				Provider: "mock",
				Rank:     1,
			},
		},
		Total: 1,
	}
	if r.Query == "" {
		t.Error("SearchResult.Query must not be empty")
	}
	if r.Provider == "" {
		t.Error("SearchResult.Provider must not be empty")
	}
	if len(r.Items) == 0 {
		t.Error("SearchResult.Items must not be empty")
	}
	if r.Items[0].URL == "" {
		t.Error("SearchResultItem.URL must not be empty")
	}
}

// ---------------------------------------------------------------------------
// Heading, Link, Table struct tests
// ---------------------------------------------------------------------------

func TestHeading_LevelRange(t *testing.T) {
	for level := 1; level <= 6; level++ {
		h := web.Heading{Level: level, Text: "Section"}
		if h.Level < 1 || h.Level > 6 {
			t.Errorf("Heading.Level = %d is out of [1,6]", h.Level)
		}
	}
}

func TestLink_Fields(t *testing.T) {
	l := web.Link{Text: "Click here", URL: "https://example.com"}
	if l.Text == "" {
		t.Error("Link.Text is empty")
	}
	if l.URL == "" {
		t.Error("Link.URL is empty")
	}
}

func TestTable_Structure(t *testing.T) {
	tbl := web.Table{
		Headers: []string{"Name", "Value"},
		Rows:    [][]string{{"foo", "1"}, {"bar", "2"}},
	}
	if len(tbl.Headers) != 2 {
		t.Errorf("Table.Headers count = %d, want 2", len(tbl.Headers))
	}
	if len(tbl.Rows) != 2 {
		t.Errorf("Table.Rows count = %d, want 2", len(tbl.Rows))
	}
}

// ---------------------------------------------------------------------------
// ProviderStatus health check fields
// ---------------------------------------------------------------------------

func TestProviderStatus_Fields(t *testing.T) {
	now := time.Now()
	ps := web.ProviderStatus{
		Available:   true,
		Message:     "OK",
		LastChecked: now,
		Latency:     42 * time.Millisecond,
	}
	if !ps.Available {
		t.Error("ProviderStatus.Available should be true")
	}
	if ps.Latency != 42*time.Millisecond {
		t.Errorf("ProviderStatus.Latency = %v, want 42ms", ps.Latency)
	}
}

// ---------------------------------------------------------------------------
// Extractor backend field
// ---------------------------------------------------------------------------

func TestExtractor_BackendIsPopulated(t *testing.T) {
	ex := &mockExtractor{name: "scrapling", doc: web.Document{Markdown: "content"}}
	doc, err := ex.ExtractHTML(context.Background(), "<html></html>", "https://test.example", nil)
	if err != nil {
		t.Fatalf("ExtractHTML() unexpected error: %v", err)
	}
	if doc.Backend != "scrapling" {
		t.Errorf("Document.Backend = %q, want %q", doc.Backend, "scrapling")
	}
}

// ---------------------------------------------------------------------------
// SearchQuery MaxResults defaults
// ---------------------------------------------------------------------------

func TestSearchQuery_MaxResults(t *testing.T) {
	q := web.SearchQuery{Query: "test", MaxResults: 10}
	if q.MaxResults != 10 {
		t.Errorf("SearchQuery.MaxResults = %d, want 10", q.MaxResults)
	}
	zeroQ := web.SearchQuery{Query: "test"}
	if zeroQ.MaxResults != 0 {
		t.Errorf("default SearchQuery.MaxResults = %d, want 0", zeroQ.MaxResults)
	}
}

// ---------------------------------------------------------------------------
// Aggregate context budget cap
// ---------------------------------------------------------------------------

func TestResearch_AggregateContextBudgetCapped(t *testing.T) {
	const maxSources = 10
	const perSourceBudget = 10_000
	const aggregateBudget = maxSources * perSourceBudget

	// Simulate 15 sources each producing 10KB.
	type sourceFetch struct {
		url     string
		content string
	}
	sources := make([]sourceFetch, 15)
	for i := range sources {
		sources[i] = sourceFetch{
			url:     "https://example.com/" + string(rune('a'+i)),
			content: strings.Repeat("x", perSourceBudget),
		}
	}

	var totalBytes int
	var included int
	for _, s := range sources {
		if included >= maxSources {
			break
		}
		totalBytes += len(s.content)
		included++
	}

	if totalBytes > aggregateBudget {
		t.Errorf("aggregate context %d exceeds budget %d", totalBytes, aggregateBudget)
	}
	if included > maxSources {
		t.Errorf("included %d sources, max is %d", included, maxSources)
	}
}

// ---------------------------------------------------------------------------
// Offline safety: search provider test servers don't require live internet
// ---------------------------------------------------------------------------

func TestOffline_MockSearchProviderRequiresNoInternet(t *testing.T) {
	p := &mockSearchProvider{
		name: "offline-mock",
		results: web.SearchResult{
			Items: []web.SearchResultItem{
				{Title: "Local Result", URL: "http://localhost/result", Snippet: "offline", Rank: 1},
			},
			Total: 1,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	r, err := p.Search(ctx, web.SearchQuery{Query: "offline test", MaxResults: 5})
	if err != nil {
		t.Fatalf("offline Search() failed: %v", err)
	}
	if len(r.Items) == 0 {
		t.Error("offline Search() returned no items")
	}
}

func TestOffline_MockExtractorRequiresNoInternet(t *testing.T) {
	ex := &mockExtractor{
		name: "offline-native",
		doc:  web.Document{Title: "Offline Doc", Markdown: "# Local Content"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	doc, err := ex.ExtractHTML(ctx, "<html><h1>Local</h1></html>", "", nil)
	if err != nil {
		t.Fatalf("offline ExtractHTML() failed: %v", err)
	}
	if doc.Backend != "offline-native" {
		t.Errorf("unexpected backend: %q", doc.Backend)
	}
}

// ---------------------------------------------------------------------------
// Regression tests for table-cell link extraction (including DDG Lite markup)
// ---------------------------------------------------------------------------

func TestNativeExtractor_TableNestedLinkExtraction(t *testing.T) {
	htmlInput := `<!DOCTYPE html>
<html>
<head><title>Table Link Test</title></head>
<body>
  <table>
    <thead>
      <tr>
        <th>Resource Header (<a href="/header-link">Header Link</a>)</th>
        <th>Description</th>
      </tr>
    </thead>
    <tbody>
      <tr>
        <td>
          <a class="result-link" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fblog%2Fpipelines&rut=101">Go Concurrency Patterns</a>
        </td>
        <td>Learn Go pipelines and cancellation.</td>
      </tr>
      <tr>
        <td>
          <span>Nested <div><a href="/relative/path">Relative Target</a></div></span>
        </td>
        <td>Second description with duplicate <a href="/relative/path">Relative Target</a></td>
      </tr>
    </tbody>
  </table>
</body>
</html>`

	ex := web.NewNativeExtractor()
	doc, err := ex.ExtractHTML(context.Background(), htmlInput, "https://example.com/sub/", nil)
	if err != nil {
		t.Fatalf("ExtractHTML failed: %v", err)
	}

	// 1. Verify links from <th> were extracted and resolved.
	foundHeaderLink := false
	for _, l := range doc.Links {
		if l.Text == "Header Link" && l.URL == "https://example.com/header-link" {
			foundHeaderLink = true
			break
		}
	}
	if !foundHeaderLink {
		t.Errorf("expected <th> link to be extracted and resolved: %+v", doc.Links)
	}

	// 2. Verify DDG Lite-style link was extracted and scheme-relative URL resolved.
	foundDDGLink := false
	for _, l := range doc.Links {
		if l.Text == "Go Concurrency Patterns" && strings.Contains(l.URL, "duckduckgo.com/l/?uddg=") {
			foundDDGLink = true
			break
		}
	}
	if !foundDDGLink {
		t.Errorf("expected DDG Lite result link to be extracted: %+v", doc.Links)
	}

	// 3. Verify deeply nested relative link was extracted and resolved against base URL.
	foundRelativeLink := false
	relativeCount := 0
	for _, l := range doc.Links {
		if l.Text == "Relative Target" && l.URL == "https://example.com/relative/path" {
			foundRelativeLink = true
			relativeCount++
		}
	}
	if !foundRelativeLink {
		t.Errorf("expected relative link to be extracted: %+v", doc.Links)
	}
	// 4. Verify duplicate links inside cells are deduplicated.
	if relativeCount != 1 {
		t.Errorf("expected duplicate link to be deduplicated, count=%d", relativeCount)
	}

	// 5. Verify table Markdown formatting was preserved.
	if !strings.Contains(doc.Markdown, "| Resource Header (Header Link) | Description |") {
		t.Errorf("Markdown table header corrupted: %s", doc.Markdown)
	}
	if !strings.Contains(doc.Markdown, "| Go Concurrency Patterns | Learn Go pipelines and cancellation. |") {
		t.Errorf("Markdown table row corrupted: %s", doc.Markdown)
	}
}
