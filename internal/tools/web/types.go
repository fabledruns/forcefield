package web

import (
	"context"
	"errors"
	"time"
)

// Common error classifications for search and retrieval.
var (
	ErrRateLimited     = errors.New("provider rate limit exceeded")
	ErrNetwork         = errors.New("network retrieval failure")
	ErrParseFailed     = errors.New("content parse failure")
	ErrInvalidKey      = errors.New("missing or invalid API key")
	ErrAccessDenied    = errors.New("access denied by remote site or policy")
	ErrRobotsDisallowed = errors.New("disallowed by robots.txt")
	ErrBudgetExceeded  = errors.New("resource budget exceeded")
	ErrBackendNotFound = errors.New("extraction backend not available")
)

// Heading represents a structured heading in a document.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// Link represents an extracted hyperlink.
type Link struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// Table represents an extracted structured table.
type Table struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

// Document is the normalized representation of retrieved and extracted content,
// produced identically by both Go native and Scrapling extraction backends.
type Document struct {
	URL          string            `json:"url"`
	CanonicalURL string            `json:"canonical_url,omitempty"`
	Title        string            `json:"title"`
	Description  string            `json:"description,omitempty"`
	Author       string            `json:"author,omitempty"`
	Published    string            `json:"published,omitempty"`
	Markdown     string            `json:"markdown"`
	RawText      string            `json:"raw_text,omitempty"`
	Headings     []Heading         `json:"headings,omitempty"`
	Links        []Link            `json:"links,omitempty"`
	Tables       []Table           `json:"tables,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	ByteCount    int               `json:"byte_count"`
	Truncated    bool              `json:"truncated"`
	Backend      string            `json:"backend"`
}

// SearchQuery represents a search request to a provider.
type SearchQuery struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

// SearchResultItem represents one ranked search result.
type SearchResultItem struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet"`
	Provider string `json:"provider"`
	Rank     int    `json:"rank"`
}

// SearchResult represents the aggregated output from a search query.
type SearchResult struct {
	Query    string             `json:"query"`
	Provider string             `json:"provider"`
	Items    []SearchResultItem `json:"items"`
	Total    int                `json:"total"`
}

// ProviderStatus captures provider availability and health metrics.
type ProviderStatus struct {
	Available   bool          `json:"available"`
	Message     string        `json:"message,omitempty"`
	LastChecked time.Time     `json:"last_checked"`
	Latency     time.Duration `json:"latency,omitempty"`
}

// SearchProvider represents a pluggable search backend.
type SearchProvider interface {
	Name() string
	Search(ctx context.Context, query SearchQuery) (SearchResult, error)
	CheckHealth(ctx context.Context) ProviderStatus
}

// Extractor is implemented by HTML extraction backends (Native Go and Scrapling).
type Extractor interface {
	Name() string
	ExtractHTML(ctx context.Context, rawHTML string, baseURL string, targets []string) (Document, error)
}
