package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"forcefield/internal/tools/web/safenet"
)

const (
	braveSearchAPIURL = "https://api.search.brave.com/res/v1/web/search"
	braveTimeout      = 15 * time.Second
	braveMaxResults   = 20
	braveEnvKey       = "BRAVE_SEARCH_API_KEY"
)

// BraveProvider queries the Brave Search REST API.
type BraveProvider struct {
	apiKey string
	client *http.Client
}

// NewBraveProvider creates a BraveProvider.
// apiKey takes precedence over the BRAVE_SEARCH_API_KEY environment variable.
func NewBraveProvider(apiKey string, sn *safenet.Client) *BraveProvider {
	if apiKey == "" {
		apiKey = os.Getenv(braveEnvKey)
	}
	var client *http.Client
	if sn != nil {
		client = sn.HTTPClient()
	} else {
		client = &http.Client{Timeout: braveTimeout}
	}
	return &BraveProvider{apiKey: apiKey, client: client}
}

func (b *BraveProvider) Name() string { return "brave" }

// Search calls the Brave Web Search API.
func (b *BraveProvider) Search(ctx context.Context, query SearchQuery) (SearchResult, error) {
	if b.apiKey == "" {
		return SearchResult{}, fmt.Errorf("%w: set %s or provide key in config", ErrInvalidKey, braveEnvKey)
	}
	if query.Query == "" {
		return SearchResult{}, fmt.Errorf("%w: empty query", ErrParseFailed)
	}

	max := query.MaxResults
	if max <= 0 || max > braveMaxResults {
		max = 10
	}

	params := url.Values{}
	params.Set("q", query.Query)
	params.Set("count", fmt.Sprintf("%d", max))
	params.Set("safesearch", "moderate")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveSearchAPIURL+"?"+params.Encode(), nil)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: build request: %s", ErrNetwork, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", b.apiKey)
	req.Header.Set("User-Agent", ddgUserAgent)

	resp, err := b.client.Do(req)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNetwork, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return SearchResult{}, ErrRateLimited
	case http.StatusForbidden, http.StatusUnauthorized:
		return SearchResult{}, fmt.Errorf("%w: check %s", ErrInvalidKey, braveEnvKey)
	}

	if resp.StatusCode != http.StatusOK {
		return SearchResult{}, fmt.Errorf("%w: HTTP %d from Brave API", ErrNetwork, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: read response: %s", ErrNetwork, err)
	}

	var apiResp braveSearchResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return SearchResult{}, fmt.Errorf("%w: JSON decode: %s", ErrParseFailed, err)
	}

	items := make([]SearchResultItem, 0, len(apiResp.Web.Results))
	for i, r := range apiResp.Web.Results {
		if i >= max {
			break
		}
		items = append(items, SearchResultItem{
			Title:    r.Title,
			URL:      r.URL,
			Snippet:  r.Description,
			Provider: b.Name(),
			Rank:     i + 1,
		})
	}

	return SearchResult{
		Query:    query.Query,
		Provider: b.Name(),
		Items:    items,
		Total:    len(items),
	}, nil
}

// CheckHealth verifies the API key is configured.
func (b *BraveProvider) CheckHealth(ctx context.Context) ProviderStatus {
	if b.apiKey == "" {
		return ProviderStatus{
			Available:   false,
			Message:     fmt.Sprintf("API key not configured; set %s", braveEnvKey),
			LastChecked: time.Now(),
		}
	}
	// Minimal search to check connectivity.
	start := time.Now()
	params := url.Values{}
	params.Set("q", "test")
	params.Set("count", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveSearchAPIURL+"?"+params.Encode(), nil)
	if err != nil {
		return ProviderStatus{Available: false, Message: err.Error(), LastChecked: time.Now()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.apiKey)

	resp, err := b.client.Do(req)
	if err != nil {
		return ProviderStatus{Available: false, Message: err.Error(), LastChecked: time.Now()}
	}
	resp.Body.Close()

	return ProviderStatus{
		Available:   resp.StatusCode == http.StatusOK,
		LastChecked: time.Now(),
		Latency:     time.Since(start),
	}
}

// braveSearchResponse models the Brave Search API JSON response.
type braveSearchResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}
