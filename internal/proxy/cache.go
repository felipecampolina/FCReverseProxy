package proxy

import (
	"container/list"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CachedResponse stores the full HTTP response needed to serve a cache HIT
// without contacting the upstream. The Body is kept in memory.
// This implementation allows caching for any HTTP method if directives permit.
type CachedResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	StoredAt   time.Time
	ExpiresAt  time.Time
	RequestID  string // Persisted request id captured from the MISS that created this entry
}

// Cache defines the basic operations for a cache.
// Get returns (response, ok, stale). If stale=true, the item exists but is expired.
type Cache interface {
	Get(key string) (resp *CachedResponse, ok bool, stale bool)
	Set(key string, resp *CachedResponse, ttl time.Duration)
	Delete(key string)
	Purge()
	Stats() CacheStats
}

// CacheStats tracks basic cache metrics.
type CacheStats struct {
	Entries   int    // Current number of items in the cache
	Hits      uint64 // Number of successful non-stale lookups
	Misses    uint64 // Number of lookups that found no entry
	Stores    uint64 // Number of inserts
	Evictions uint64 // Number of LRU evictions
}

// lruCache is a simple thread-safe LRU cache with TTL per item.
type lruCache struct {
	mu         sync.Mutex
	lruList    *list.List
	items      map[string]*list.Element
	maxEntries int
	stats      CacheStats
}

// lruEntry wraps a cache key and its CachedResponse for storage in the LRU list.
type lruEntry struct {
	key string
	val *CachedResponse
}

// context key for cached request key
type cacheKeyCtxKey struct{}
type upstreamTargetCtxKey struct{}
// add context key for request start time (end-to-end measurement)
type startTimeCtxKey struct{}

// Globally configurable default cache TTL (used when upstream provides no directives).
var defaultCacheTTL atomic.Value // stores time.Duration

func init() {
	defaultCacheTTL.Store(60 * time.Second)
}

// SetDefaultCacheTTL overrides the global default TTL used when no upstream cache
// directives are present (e.g., no max-age/s-maxage/Expires), or when a cache Set()
// is called with ttl <= 0. Non-positive values reset to 60s.
func SetDefaultCacheTTL(d time.Duration) {
	if d <= 0 {
		d = 60 * time.Second
	}
	defaultCacheTTL.Store(d)
}

// getDefaultCacheTTL returns the currently configured default cache TTL.
func getDefaultCacheTTL() time.Duration {
	if v := defaultCacheTTL.Load(); v != nil {
		if d, ok := v.(time.Duration); ok {
			return d
		}
	}
	return 60 * time.Second
}

// NewLRUCache creates a new LRU cache with a maximum number of entries.
// If maxEntries <= 0, it defaults to 1024.
func NewLRUCache(maxEntries int) Cache {
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	return &lruCache{
		lruList:    list.New(),
		items:      make(map[string]*list.Element),
		maxEntries: maxEntries,
	}
}

// Get retrieves a cached response by key.
// It returns the response, whether it exists, and whether it is stale (expired).
func (cache *lruCache) Get(cacheKey string) (*CachedResponse, bool, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if element, found := cache.items[cacheKey]; found {
		entry := element.Value.(*lruEntry)

		// Touch the element to mark it as most recently used.
		cache.lruList.MoveToFront(element)

		// If expired, signal stale=true while still returning the entry for validation use.
		if time.Now().After(entry.val.ExpiresAt) {
			return entry.val, true, true
		}

		cache.stats.Hits++
		return entry.val, true, false
	}

	cache.stats.Misses++
	return nil, false, false
}

// Set stores a response in the cache with a specified TTL.
// If ttl <= 0, the configured default TTL is applied.
func (cache *lruCache) Set(cacheKey string, response *CachedResponse, ttl time.Duration) {
	if ttl <= 0 {
		ttl = getDefaultCacheTTL()
	}
	response.ExpiresAt = time.Now().Add(ttl)

	cache.mu.Lock()
	defer cache.mu.Unlock()

	if element, found := cache.items[cacheKey]; found {
		// Update the existing entry and mark it as most recently used.
		entry := element.Value.(*lruEntry)
		entry.val = response
		cache.lruList.MoveToFront(element)
	} else {
		// Insert a new entry at the front (most recently used).
		element := cache.lruList.PushFront(&lruEntry{key: cacheKey, val: response})
		cache.items[cacheKey] = element
		cache.stats.Stores++

		// Enforce capacity using LRU eviction policy.
		if cache.lruList.Len() > cache.maxEntries {
			cache.removeOldest() // Evict the least recently used item (LRU)
		}
	}

	cache.stats.Entries = cache.lruList.Len()
}

// removeOldest evicts the least recently used entry (at the back of the list).
func (cache *lruCache) removeOldest() {
	element := cache.lruList.Back()
	if element != nil {
		cache.removeElement(element)
	}
}

// removeElement removes a specific list element and updates the map and stats.
func (cache *lruCache) removeElement(element *list.Element) {
	cache.lruList.Remove(element)
	entry := element.Value.(*lruEntry)
	delete(cache.items, entry.key)
	cache.stats.Evictions++
}

// Delete removes a specific key from the cache.
func (cache *lruCache) Delete(cacheKey string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if element, found := cache.items[cacheKey]; found {
		cache.removeElement(element)
		cache.stats.Entries = cache.lruList.Len()
	}
}

// Purge clears all entries from the cache.
// It is like a reset of the cache state.
func (cache *lruCache) Purge() {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	cache.lruList = list.New()
	cache.items = make(map[string]*list.Element)
	cache.stats.Entries = 0
}

// Stats returns current cache statistics.
func (cache *lruCache) Stats() CacheStats {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.stats
}

// ===== HTTP Cache Helpers =====

// hopHeaders lists hop-by-hop headers that should not be cached or forwarded as-is.
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

// isCacheableRequest determines if a request is cacheable based on its headers.
// This implementation allows any HTTP method unless "no-store"/"no-cache" is present,
// and avoids caching authenticated requests unless explicitly marked "public".
func isCacheableRequest(req *http.Request) bool {
	cacheControl := parseCacheControl(req.Header.Get("Cache-Control"))

	// Respect explicit client directives.
	if _, ok := cacheControl["no-store"]; ok {
		return false
	}
	if _, ok := cacheControl["no-cache"]; ok {
		return false
	}

	// Heuristic: avoid caching when Authorization is present unless "public" is provided.
	// In production, consider stricter rules based on method and other headers.
	if req.Header.Get("Authorization") != "" {
		if _, isPublic := cacheControl["public"]; !isPublic {
			return false
		}
	}
	return true
}

// isCacheableResponse validates if a response is cacheable and computes its TTL.
// It returns (ttl, ok). If ok=false, the response must not be cached.
func isCacheableResponse(response *http.Response) (ttl time.Duration, ok bool) {
	// Only cache common cacheable status codes.
	switch response.StatusCode {
	case 200, 203, 204, 300, 301, 404, 410:
		// Cacheable statuses
	default:
		return 0, false
	}

	cacheControl := parseCacheControl(response.Header.Get("Cache-Control"))

	// Respect server directive to avoid storage.
	if _, noStore := cacheControl["no-store"]; noStore {
		return 0, false
	}

	// Prefer s-maxage (shared caches) over max-age when present.
	if sMaxAge, has := cacheControl["s-maxage"]; has {
		if d, err := time.ParseDuration(sMaxAge + "s"); err == nil {
			return d, true
		}
	}
	if maxAge, has := cacheControl["max-age"]; has {
		if d, err := time.ParseDuration(maxAge + "s"); err == nil {
			return d, true
		}
	}

	// Fallback to Expires header if present and valid.
	if expires := response.Header.Get("Expires"); expires != "" {
		if expiryTime, err := http.ParseTime(expires); err == nil {
			if expiryTime.After(time.Now()) {
				return time.Until(expiryTime), true
			}
		}
	}

	// Fallback to configured default TTL when no upstream directives exist.
	return getDefaultCacheTTL(), true
}

// parseCacheControl splits a Cache-Control header into a directive map.
// Keys are lowercase, and values are unquoted when provided (e.g., max-age=60).
func parseCacheControl(headerValue string) map[string]string {
	directives := make(map[string]string)
	if headerValue == "" {
		return directives
	}

	segments := strings.Split(headerValue, ",")
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}

		keyValue := strings.SplitN(segment, "=", 2)
		key := strings.ToLower(strings.TrimSpace(keyValue[0]))

		if len(keyValue) == 2 {
			directives[key] = strings.Trim(keyValue[1], "\" ")
		} else {
			directives[key] = ""
		}
	}
	return directives
}

// buildCacheKey generates a stable cache key for a request.
// It combines method, scheme, host, path, query, and a few Vary-like headers.
func buildCacheKey(req *http.Request) string {
	keyBuilder := strings.Builder{}
	keyBuilder.WriteString(req.Method)
	keyBuilder.WriteString(" ")
	keyBuilder.WriteString(req.URL.Scheme)
	keyBuilder.WriteString("://")
	keyBuilder.WriteString(req.Host)
	keyBuilder.WriteString(singleJoiningSlash("", req.URL.Path))
	if req.URL.RawQuery != "" {
		keyBuilder.WriteString("?")
		keyBuilder.WriteString(req.URL.RawQuery)
	}
	// Include common Vary dimensions to reduce collisions across content variants.
	keyBuilder.WriteString("|a=")
	keyBuilder.WriteString(strings.TrimSpace(req.Header.Get("Accept")))
	keyBuilder.WriteString("|ae=")
	keyBuilder.WriteString(strings.TrimSpace(req.Header.Get("Accept-Encoding")))
	return keyBuilder.String()
}

// Checks if the client explicitly requested no-cache.
func clientNoCache(req *http.Request) bool {
	directives := parseCacheControl(req.Header.Get("Cache-Control"))
	if _, ok := directives["no-cache"]; ok {
		return true
	}
	if _, ok := directives["no-store"]; ok {
		return true
	}
	if strings.EqualFold(req.Header.Get("Pragma"), "no-cache") {
		return true
	}
	return false
}
