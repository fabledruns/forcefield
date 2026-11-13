package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"forcefield/internal/tools/web/safenet"
)

const (
	tavilyAPIURL   = "https://api.tavily.com/search"
	tavilyTimeout  = 20 * time.Second
	tavilyMaxResults = 20
	tavilyEnvKey   = "TAVILY_API_KEY"
)

// TavilyProvider queries the Tavily AI Search REST API.
type TavilyProvider struct {
	apiKey string
	client *http.Client
}

// NewTavilyProvider creates a TavilyProvider.
// apiKey takes precedence over the TAVILY_API_KEY environment variable.
func NewTavilyProvider(apiKey string, sn *safenet.Client) *TavilyProvider {
	if apiKey == "" {
		apiKey = os.Getenv(tavilyEnvKey)
	}
	var client *http.Client
	if sn != nil {
		client = sn.HTTPClient()
	} else {
		client = &http.Client{Timeout: tavilyTimeout}
	}
	return &TavilyProvider{apiKey: apiKey, client: client}
}

func (t *TavilyProvider) Name() string { return "tavily" }

// Search calls the Tavily search API.
func (t *TavilyProvider) Search(ctx context.Context, query SearchQuery) (SearchResult, error) {
	if t.apiKey == "" {
		return SearchResult{}, fmt.Errorf("%w: set %s or provide key in config", ErrInvalidKey, tavilyEnvKey)
	}
	if query.Query == "" {
		return SearchResult{}, fmt.Errorf("%w: empty query", ErrParseFailed)
	}

	max := query.MaxResults
	if max <= 0 || max > tavilyMaxResults {
		max = 5
	}

	reqBody := tavilyRequest{
		APIKey:          t.apiKey,
		Query:           query.Query,
		SearchDepth:     "basic",
		MaxResults:      max,
		IncludeAnswer:   false,
		IncludeRawContent: false,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: marshal request: %s", ErrParseFailed, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyAPIURL, bytes.NewReader(payload))
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: build request: %s", ErrNetwork, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ddgUserAgent)

	resp, err := t.client.Do(req)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: %s", ErrNetwork, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return SearchResult{}, ErrRateLimited
	case http.StatusForbidden, http.StatusUnauthorized:
		return SearchResult{}, fmt.Errorf("%w: check %s", ErrInvalidKey, tavilyEnvKey)
	}

	if resp.StatusCode != http.StatusOK {
		return SearchResult{}, fmt.Errorf("%w: HTTP %d from Tavily API", ErrNetwork, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: read response: %s", ErrNetwork, err)
	}

	var apiResp tavilyResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return SearchResult{}, fmt.Errorf("%w: JSON decode: %s", ErrParseFailed, err)
	}

	if apiResp.Error != "" {
		return SearchResult{}, fmt.Errorf("%w: Tavily error: %s", ErrParseFailed, apiResp.Error)
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
			Provider: t.Name(),
			Rank:     i + 1,
		})
	}

	return SearchResult{
		Query:    query.Query,
		Provider: t.Name(),
		Items:    items,
		Total:    len(items),
	}, nil
}

// CheckHealth verifies the API key is configured and the endpoint is reachable.
func (t *TavilyProvider) CheckHealth(ctx context.Context) ProviderStatus {
	if t.apiKey == "" {
		return ProviderStatus{
			Available:   false,
			Message:     fmt.Sprintf("API key not configured; set %s", tavilyEnvKey),
			LastChecked: time.Now(),
		}
	}
	start := time.Now()
	// Minimal request to verify key validity.
	reqBody := tavilyRequest{
		APIKey:      t.apiKey,
		Query:       "test",
		MaxResults:  1,
		SearchDepth: "basic",
	}
	payload, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyAPIURL, bytes.NewReader(payload))
	if err != nil {
		return ProviderStatus{Available: false, Message: err.Error(), LastChecked: time.Now()}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
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

// tavilyRequest is the JSON body sent to the Tavily API.
type tavilyRequest struct {
	APIKey            string   `json:"api_key"`
	Query             string   `json:"query"`
	SearchDepth       string   `json:"search_depth,omitempty"`
	MaxResults        int      `json:"max_results,omitempty"`
	IncludeAnswer     bool     `json:"include_answer,omitempty"`
	IncludeRawContent bool     `json:"include_raw_content,omitempty"`
	IncludeDomains    []string `json:"include_domains,omitempty"`
	ExcludeDomains    []string `json:"exclude_domains,omitempty"`
}

// tavilyResponse models the Tavily API JSON response.
type tavilyResponse struct {
	Query   string         `json:"query"`
	Results []tavilyResult `json:"results"`
	Error   string         `json:"error,omitempty"`
}

type tavilyResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}
