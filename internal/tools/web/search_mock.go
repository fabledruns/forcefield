package web

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MockProvider is a deterministic search provider for unit testing and offline development.
// Results are pre-seeded and returned in a predictable order.
type MockProvider struct {
	mu          sync.RWMutex
	name        string
	results     map[string][]SearchResultItem
	healthOK    bool
	callCount   int
	lastQuery   string
	errToReturn error
}

// NewMockProvider creates a MockProvider with the given name and seed data.
// If seedResults is nil, searches return empty results.
func NewMockProvider(name string, seedResults map[string][]SearchResultItem) *MockProvider {
	if name == "" {
		name = "mock"
	}
	if seedResults == nil {
		seedResults = make(map[string][]SearchResultItem)
	}
	return &MockProvider{
		name:     name,
		results:  seedResults,
		healthOK: true,
	}
}

func (m *MockProvider) Name() string { return m.name }

// Search returns pre-seeded results for the query, or empty results if none seeded.
// If an error has been configured via SetError, it is returned instead.
func (m *MockProvider) Search(ctx context.Context, query SearchQuery) (SearchResult, error) {
	m.mu.Lock()
	m.callCount++
	m.lastQuery = query.Query
	err := m.errToReturn
	m.mu.Unlock()

	if ctx.Err() != nil {
		return SearchResult{}, ctx.Err()
	}

	if err != nil {
		return SearchResult{}, err
	}

	if query.Query == "" {
		return SearchResult{}, fmt.Errorf("%w: empty query", ErrParseFailed)
	}

	m.mu.RLock()
	seeded := m.results[query.Query]
	m.mu.RUnlock()

	max := query.MaxResults
	if max <= 0 {
		max = 10
	}

	items := make([]SearchResultItem, 0, len(seeded))
	for i, item := range seeded {
		if i >= max {
			break
		}
		item.Provider = m.name
		item.Rank = i + 1
		items = append(items, item)
	}

	return SearchResult{
		Query:    query.Query,
		Provider: m.name,
		Items:    items,
		Total:    len(items),
	}, nil
}

// CheckHealth returns a healthy status unless MarkUnhealthy has been called.
func (m *MockProvider) CheckHealth(ctx context.Context) ProviderStatus {
	m.mu.RLock()
	ok := m.healthOK
	m.mu.RUnlock()

	msg := ""
	if !ok {
		msg = "mock provider marked unhealthy"
	}
	return ProviderStatus{
		Available:   ok,
		Message:     msg,
		LastChecked: time.Now(),
		Latency:     1 * time.Microsecond,
	}
}

// Seed adds or replaces pre-seeded results for a specific query string.
// Safe for concurrent use.
func (m *MockProvider) Seed(query string, items []SearchResultItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results[query] = items
}

// SetError configures the provider to return err on the next Search call(s).
// Pass nil to clear the error.
func (m *MockProvider) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errToReturn = err
}

// MarkUnhealthy makes CheckHealth return an unhealthy status.
func (m *MockProvider) MarkUnhealthy() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthOK = false
}

// MarkHealthy makes CheckHealth return a healthy status.
func (m *MockProvider) MarkHealthy() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthOK = true
}

// CallCount returns the number of times Search has been called.
// Useful for verifying caching and deduplication in tests.
func (m *MockProvider) CallCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.callCount
}

// LastQuery returns the most recent query string passed to Search.
func (m *MockProvider) LastQuery() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastQuery
}

// Reset clears call count, last query, error, and optionally seeds.
func (m *MockProvider) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount = 0
	m.lastQuery = ""
	m.errToReturn = nil
	m.healthOK = true
}
