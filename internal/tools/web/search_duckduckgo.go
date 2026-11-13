package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"forcefield/internal/tools/web/safenet"
)

const (
	ddgLiteURL   = "https://lite.duckduckgo.com/lite/"
	ddgTimeout   = 15 * time.Second
	ddgUserAgent = "Forcefield/1.0 (compatible; +https://github.com/fabledruns/forcefield)"
	ddgMaxResult = 20
)

// DuckDuckGoProvider queries DuckDuckGo Lite without an API key.
type DuckDuckGoProvider struct {
	client *http.Client
}

// NewDuckDuckGoProvider creates a DuckDuckGoProvider backed by the SafeNet HTTP client.
func NewDuckDuckGoProvider(sn *safenet.Client) *DuckDuckGoProvider {
	var client *http.Client
	if sn != nil {
		client = sn.HTTPClient()
	} else {
		client = &http.Client{Timeout: ddgTimeout}
	}
	return &DuckDuckGoProvider{client: client}
}

func (d *DuckDuckGoProvider) Name() string { return "duckduckgo" }

// Search queries DuckDuckGo Lite and returns structured results.
func (d *DuckDuckGoProvider) Search(ctx context.Context, query SearchQuery) (SearchResult, error) {
	if query.Query == "" {
		return SearchResult{}, fmt.Errorf("%w: empty query", ErrParseFailed)
	}

	max := query.MaxResults
	if max <= 0 || max > ddgMaxResult {
		max = 10
	}

	// Build form-encoded POST body for DDG Lite.
	form := url.Values{}
	form.Set("q", query.Query)
	form.Set("kl", "wt-wt") // worldwide
	form.Set("df", "")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ddgLiteURL, strings.NewReader(form.Encode()))
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: build request: %s", ErrNetwork, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ddgUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := d.client.Do(req)
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
		return SearchResult{}, fmt.Errorf("%w: HTTP %d", ErrNetwork, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: read response: %s", ErrNetwork, err)
	}

	items, err := parseDDGLite(string(body), max)
	if err != nil {
		return SearchResult{}, err
	}

	for i := range items {
		items[i].Provider = d.Name()
		items[i].Rank = i + 1
	}

	return SearchResult{
		Query:    query.Query,
		Provider: d.Name(),
		Items:    items,
		Total:    len(items),
	}, nil
}

// CheckHealth checks DuckDuckGo reachability.
func (d *DuckDuckGoProvider) CheckHealth(ctx context.Context) ProviderStatus {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://lite.duckduckgo.com/", nil)
	if err != nil {
		return ProviderStatus{Available: false, Message: err.Error(), LastChecked: time.Now()}
	}

	resp, err := d.client.Do(req)
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

// parseDDGLite parses the HTML returned by DDG Lite into SearchResultItems.
// DDG Lite produces simple <table> rows with links and snippets.
func parseDDGLite(body string, max int) ([]SearchResultItem, error) {
	// Use our native extractor to parse the HTML.
	ex := &nativeExtractor{}
	doc, err := ex.ExtractHTML(context.Background(), body, ddgLiteURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrParseFailed, err)
	}

	var items []SearchResultItem
	seenURLs := make(map[string]bool)

	for _, link := range doc.Links {
		if len(items) >= max {
			break
		}
		// Unwrap DDG redirect URLs (/l/?uddg=...).
		rawURL := unwrapDDGRedirect(link.URL)
		if rawURL == "" || seenURLs[rawURL] {
			continue
		}
		// Skip DDG's own navigation links.
		if strings.Contains(rawURL, "duckduckgo.com") {
			continue
		}
		seenURLs[rawURL] = true
		items = append(items, SearchResultItem{
			Title:   link.Text,
			URL:     rawURL,
			Snippet: "", // snippets not reliably parseable from links alone
		})
	}

	if len(items) == 0 {
		// If no links found, it may be a parse failure or empty results.
		if !strings.Contains(body, "lite.duckduckgo.com") {
			return nil, fmt.Errorf("%w: unexpected response format", ErrParseFailed)
		}
	}

	return items, nil
}

// unwrapDDGRedirect extracts the real URL from a DDG /l/?uddg=... redirect.
func unwrapDDGRedirect(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Path == "/l/" {
		if uddg := u.Query().Get("uddg"); uddg != "" {
			decoded, err := url.QueryUnescape(uddg)
			if err == nil {
				return decoded
			}
		}
	}
	return raw
}

// ddgJSONResult is used for the JSON API fallback (not currently used but
// kept for future extension).
type ddgJSONResult struct {
	AbstractURL   string `json:"AbstractURL"`
	AbstractText  string `json:"AbstractText"`
	RelatedTopics []struct {
		FirstURL string `json:"FirstURL"`
		Text     string `json:"Text"`
	} `json:"RelatedTopics"`
}

var _ = json.Marshal // keep import used
var _ = os.Getenv    // keep import used (used in other providers)
var _ = fmt.Sprintf  // keep used
