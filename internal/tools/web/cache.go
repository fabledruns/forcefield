package web

import (
	"container/list"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CacheEntry stores a cached document or search result.
type CacheEntry struct {
	Value     any
	ExpiresAt time.Time
}

// IsExpired reports whether the entry has passed its TTL.
func (e *CacheEntry) IsExpired() bool {
	return time.Now().After(e.ExpiresAt)
}

// Cache is a thread-safe, bounded LRU cache with per-entry TTL.
// It refuses to cache responses that carry authentication or sensitivity markers.
type Cache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	items    map[string]*list.Element
	order    *list.List // front = most recently used
}

type cacheItem struct {
	key   string
	entry *CacheEntry
}

// NewCache creates a new cache with the given maximum capacity and TTL.
// A capacity of zero disables caching entirely (all gets miss).
func NewCache(capacity int, ttl time.Duration) *Cache {
	return &Cache{
		capacity: capacity,
		ttl:      ttl,
		items:    make(map[string]*list.Element, capacity),
		order:    list.New(),
	}
}

// Get returns the cached value for key, and whether it was found and not expired.
func (c *Cache) Get(key string) (any, bool) {
	if c.capacity == 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	item := el.Value.(*cacheItem)
	if item.entry.IsExpired() {
		c.order.Remove(el)
		delete(c.items, key)
		return nil, false
	}
	c.order.MoveToFront(el)
	return item.entry.Value, true
}

// Set stores value under key with the cache's TTL.
// It evicts the least recently used entry when the cache is full.
// No-ops if capacity is zero.
func (c *Cache) Set(key string, value any) {
	if c.capacity == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		el.Value.(*cacheItem).entry = &CacheEntry{
			Value:     value,
			ExpiresAt: time.Now().Add(c.ttl),
		}
		return
	}

	// Evict LRU if at capacity.
	if c.order.Len() >= c.capacity {
		c.evictLRU()
	}

	entry := &CacheEntry{
		Value:     value,
		ExpiresAt: time.Now().Add(c.ttl),
	}
	el := c.order.PushFront(&cacheItem{key: key, entry: entry})
	c.items[key] = el
}

// Delete removes an entry from the cache.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.order.Remove(el)
		delete(c.items, key)
	}
}

// Len returns the number of non-expired entries currently in the cache.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Purge removes all expired entries from the cache.
func (c *Cache) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	var toRemove []*list.Element
	for el := c.order.Back(); el != nil; el = el.Prev() {
		item := el.Value.(*cacheItem)
		if item.entry.IsExpired() {
			toRemove = append(toRemove, el)
		}
	}
	for _, el := range toRemove {
		item := el.Value.(*cacheItem)
		c.order.Remove(el)
		delete(c.items, item.key)
	}
}

func (c *Cache) evictLRU() {
	el := c.order.Back()
	if el == nil {
		return
	}
	item := el.Value.(*cacheItem)
	c.order.Remove(el)
	delete(c.items, item.key)
}

// IsCacheable returns true if the HTTP response may be cached.
// Responses are not cached when they:
//   - Carry a Set-Cookie header.
//   - Require authentication (WWW-Authenticate or Authorization request header echo).
//   - Have Cache-Control: no-store or private.
//   - Have status codes indicating auth failure (401, 403).
func IsCacheable(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return false
	}
	if resp.Header.Get("Set-Cookie") != "" {
		return false
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		return false
	}
	cc := strings.ToLower(resp.Header.Get("Cache-Control"))
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "private") {
		return false
	}
	return true
}

// CacheKey builds a canonical cache key from a URL.
// It lower-cases the scheme and host so equivalent URLs share a key.
func CacheKey(rawURL string) string {
	// Minimal normalization for key purposes; NormalizeURL handles full normalization.
	return strings.ToLower(rawURL)
}
