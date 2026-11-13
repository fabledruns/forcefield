package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"forcefield/internal/tools/web/safenet"
)

const (
	searxngDefaultBase = "http://localhost:8080"
	searxngTimeout     = 15 * time.Second
	searxngMaxResults  = 20
)

// SearXNGProvider connects to a self-hosted or remote SearXNG instance.
type SearXNGProvider struct {
	baseURL string
	client  *http.Client
}

// NewSearXNGProvider creates a SearXNGProvider connecting to baseURL.
// If baseURL is empty, it defaults to localhost:8080.
func NewSearXNGProvider(baseURL string, sn *safenet.Client) *SearXNGProvider {
	if baseURL == "" {
		baseURL = searxngDefaultBase
	}
	// Trim trailing slash.
	for len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}

	var client *http.Client
	if sn != nil {
		client = sn.HTTPClient()
	} else {
		client = &http.Client{Timeout: searxngTimeout}
	}
	return &SearXNGProvider{baseURL: baseURL, client: client}
}

func (s *SearXNGProvider) Name() string { return "searxng" }

// Search sends a JSON query to the SearXNG /search endpoint.
func (s *SearXNGProvider) Search(ctx context.Context, query SearchQuery) (SearchResult, error) {
	if query.Query == "" {
		return SearchResult{}, fmt.Errorf("%w: empty query", ErrParseFailed)
	}

	max := query.MaxResults
	if max <= 0 || max > searxngMaxResults {
		max = 10
	}

	endpoint := s.baseURL + "/search"
	params := url.Values{}
	params.Set("q", query.Query)
	params.Set("format", "json")
	params.Set("pageno", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: build request: %s", ErrNetwork, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", ddgUserAgent)

	resp, err := s.client.Do(req)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNetwork, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return SearchResult{}, ErrRateLimited
	case http.StatusForbidden, http.StatusUnauthorized:
		return SearchResult{}, ErrAccessDenied
	}

	if resp.StatusCode != http.StatusOK {
		return SearchResult{}, fmt.Errorf("%w: HTTP %d from SearXNG", ErrNetwork, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: read response: %s", ErrNetwork, err)
	}

	var apiResp searxngResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return SearchResult{}, fmt.Errorf("%w: JSON decode: %s", ErrParseFailed, err)
	}

	items := make([]SearchResultItem, 0, len(apiResp.Results))
	for i, r := range apiResp.Results {
		if i >= max {
			break
		}
		items = append(items, SearchResultItem{
			Title:    r.Title,
			URL:      r.URL,
			Snippet:  r.Content,
			Provider: s.Name(),
			Rank:     i + 1,
		})
	}

	return SearchResult{
		Query:    query.Query,
		Provider: s.Name(),
		Items:    items,
		Total:    len(items),
	}, nil
}

// CheckHealth pings the SearXNG instance.
func (s *SearXNGProvider) CheckHealth(ctx context.Context) ProviderStatus {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/healthz", nil)
	if err != nil {
		return ProviderStatus{Available: false, Message: err.Error(), LastChecked: time.Now()}
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return ProviderStatus{Available: false, Message: err.Error(), LastChecked: time.Now()}
	}
	resp.Body.Close()

	return ProviderStatus{
		Available:   resp.StatusCode < 500,
		LastChecked: time.Now(),
		Latency:     time.Since(start),
	}
}

// searxngResponse models the JSON response from SearXNG.
type searxngResponse struct {
	Query   string          `json:"query"`
	Results []searxngResult `json:"results"`
}

type searxngResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
	Score   float64 `json:"score"`
}
