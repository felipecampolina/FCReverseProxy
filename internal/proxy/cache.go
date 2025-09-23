package proxy

import (
	"container/list"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CachedResponse stores the necessary data to respond without contacting the upstream.
// The Body is kept in memory. For large payloads, consider using a disk-based cache.
// Only GET requests are cached in this example.
type CachedResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	StoredAt   time.Time
	ExpiresAt  time.Time
}

// Cache defines the basic operations for a cache.
// Get returns (response, ok, stale). If stale=true, the item exists but has expired.
type Cache interface {
	Get(key string) (resp *CachedResponse, ok bool, stale bool)
	Set(key string, resp *CachedResponse, ttl time.Duration)
	Delete(key string)
	Purge()
	Stats() CacheStats
}

type CacheStats struct {
	Entries   int
	Hits      uint64
	Misses    uint64
	Stores    uint64
	Evictions uint64
}

// lruCache is a simple thread-safe LRU cache with TTL per item.
type lruCache struct {
	mu         sync.Mutex
	ll         *list.List
	items      map[string]*list.Element
	maxEntries int
	stats      CacheStats
}

type lruEntry struct {
	key string
	val *CachedResponse
}

// Creates a new LRU cache with a maximum number of entries.
func NewLRUCache(maxEntries int) Cache {
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	return &lruCache{
		ll:         list.New(),
		items:      make(map[string]*list.Element),
		maxEntries: maxEntries,
	}
}

// Retrieves a cached response by key.
func (c *lruCache) Get(key string) (*CachedResponse, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ele, ok := c.items[key]; ok {
		ent := ele.Value.(*lruEntry)
		// Move to the front (most recently used)
		c.ll.MoveToFront(ele)
		if time.Now().After(ent.val.ExpiresAt) {
			return ent.val, true, true
		}
		c.stats.Hits++
		return ent.val, true, false
	}
	c.stats.Misses++
	return nil, false, false
}

// Stores a response in the cache with a specified TTL.
func (c *lruCache) Set(key string, resp *CachedResponse, ttl time.Duration) {
	if ttl <= 0 {
		// Default TTL: 60 seconds if no positive TTL is provided
		ttl = 60 * time.Second
	}
	resp.ExpiresAt = time.Now().Add(ttl)

	c.mu.Lock()
	defer c.mu.Unlock()
	if ele, ok := c.items[key]; ok {
		ent := ele.Value.(*lruEntry)
		ent.val = resp
		c.ll.MoveToFront(ele)
	} else {
		ele := c.ll.PushFront(&lruEntry{key: key, val: resp})
		c.items[key] = ele
		c.stats.Stores++
		if c.ll.Len() > c.maxEntries {
			c.removeOldest()
		}
	}
	c.stats.Entries = c.ll.Len()
}

// Removes the oldest entry from the cache.
func (c *lruCache) removeOldest() {
	ele := c.ll.Back()
	if ele != nil {
		c.removeElement(ele)
	}
}

// Removes a specific element from the cache.
func (c *lruCache) removeElement(e *list.Element) {
	c.ll.Remove(e)
	ent := e.Value.(*lruEntry)
	delete(c.items, ent.key)
	c.stats.Evictions++
}

// Deletes a specific key from the cache.
func (c *lruCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ele, ok := c.items[key]; ok {
		c.removeElement(ele)
		c.stats.Entries = c.ll.Len()
	}
}

// Clears all entries from the cache.
func (c *lruCache) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll = list.New()
	c.items = make(map[string]*list.Element)
	c.stats.Entries = 0
}

// Returns cache statistics.
func (c *lruCache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// ===== HTTP Cache Helpers =====

// Headers that should not be cached (hop-by-hop headers).
var hopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

// Determines if a request is cacheable (only GET requests without no-store/no-cache directives).
func isCacheableRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	cc := parseCacheControl(r.Header.Get("Cache-Control"))
	if _, ok := cc["no-store"]; ok {
		return false
	}
	if _, ok := cc["no-cache"]; ok {
		return false
	}
	// Heuristic: avoid caching if Authorization is present unless explicitly allowed
	if r.Header.Get("Authorization") != "" {
		if _, pub := cc["public"]; !pub {
			return false
		}
	}
	return true
}

// Validates if a response is cacheable based on basic directives.
func isCacheableResponse(resp *http.Response) (ttl time.Duration, ok bool) {
	// Simple status validation
	switch resp.StatusCode {
	case 200, 203, 204, 300, 301, 404, 410:
		// Cacheable statuses
	default:
		return 0, false
	}

	cc := parseCacheControl(resp.Header.Get("Cache-Control"))
	if _, noStore := cc["no-store"]; noStore {
		return 0, false
	}
	if smax, has := cc["s-maxage"]; has {
		if d, err := time.ParseDuration(smax + "s"); err == nil {
			return d, true
		}
	}
	if max, has := cc["max-age"]; has {
		if d, err := time.ParseDuration(max + "s"); err == nil {
			return d, true
		}
	}
	// Expires header
	if exp := resp.Header.Get("Expires"); exp != "" {
		if t, err := http.ParseTime(exp); err == nil {
			if t.After(time.Now()) {
				return time.Until(t), true
			}
		}
	}
	// Default heuristic TTL (60 seconds)
	return 60 * time.Second, true
}

// Parses Cache-Control headers into a map of directives.
func parseCacheControl(v string) map[string]string {
	m := make(map[string]string)
	if v == "" {
		return m
	}
	parts := strings.Split(v, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, "=", 2)
		k := strings.ToLower(strings.TrimSpace(kv[0]))
		if len(kv) == 2 {
			m[k] = strings.Trim(kv[1], "\" ")
		} else {
			m[k] = ""
		}
	}
	return m
}

// Generates a deterministic cache key for a request.
func buildCacheKey(r *http.Request) string {
	b := strings.Builder{}
	b.WriteString(r.Method)
	b.WriteString(" ")
	b.WriteString(r.URL.Scheme)
	b.WriteString("://")
	b.WriteString(r.Host)
	b.WriteString(singleJoiningSlash("", r.URL.Path))
	if r.URL.RawQuery != "" {
		b.WriteString("?")
		b.WriteString(r.URL.RawQuery)
	}
	b.WriteString("|a=")
	b.WriteString(strings.TrimSpace(r.Header.Get("Accept")))
	b.WriteString("|ae=")
	b.WriteString(strings.TrimSpace(r.Header.Get("Accept-Encoding")))
	return b.String()
}
