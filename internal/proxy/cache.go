package proxy

import (
	"container/list"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CachedResponse guarda o que precisamos para responder sem tocar o upstream.
// Body é mantido em memória. Para payloads muito grandes, considere um cache em disco.
//
// Apenas GET é cacheado neste exemplo.

type CachedResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	StoredAt   time.Time
	ExpiresAt  time.Time
}

// Cache define as operações básicas do cache.
// Get retorna (resp, ok, stale). Se stale=true, o item existe mas expirou.

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

// lruCache é uma LRU threadsafe simples com TTL por item.

type lruCache struct {
	mu           sync.Mutex
	ll           *list.List
	items        map[string]*list.Element
	maxEntries   int
	stats        CacheStats
}

type lruEntry struct {
	key  string
	val  *CachedResponse
}

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

func (c *lruCache) Get(key string) (*CachedResponse, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ele, ok := c.items[key]; ok {
		ent := ele.Value.(*lruEntry)
		// move para frente (mais recente)
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

func (c *lruCache) Set(key string, resp *CachedResponse, ttl time.Duration) {
	if ttl <= 0 {
		// default: 60s se não houver TTL positivo
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

func (c *lruCache) removeOldest() {
	ele := c.ll.Back()
	if ele != nil {
		c.removeElement(ele)
	}
}

func (c *lruCache) removeElement(e *list.Element) {
	c.ll.Remove(e)
	ent := e.Value.(*lruEntry)
	delete(c.items, ent.key)
	c.stats.Evictions++
}

func (c *lruCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ele, ok := c.items[key]; ok {
		c.removeElement(ele)
		c.stats.Entries = c.ll.Len()
	}
}

func (c *lruCache) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll = list.New()
	c.items = make(map[string]*list.Element)
	c.stats.Entries = 0
}

func (c *lruCache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// ===== Helpers de cache HTTP =====

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

// isCacheableRequest: apenas GET e sem no-store/no-cache explícito.
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
	// Heurística: se tem Authorization, evite cachear (a não ser que explicitamente permitido)
	if r.Header.Get("Authorization") != "" {
		if _, pub := cc["public"]; !pub {
			return false
		}
	}
	return true
}

// isCacheableResponse valida diretivas básicas de resposta.
func isCacheableResponse(resp *http.Response) (ttl time.Duration, ok bool) {
	// status simples
	switch resp.StatusCode {
	case 200, 203, 204, 300, 301, 404, 410:
		// ok
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
	// Expires
	if exp := resp.Header.Get("Expires"); exp != "" {
		if t, err := http.ParseTime(exp); err == nil {
			if t.After(time.Now()) {
				return time.Until(t), true
			}
		}
	}
	// heurística default (60s)
	return 60 * time.Second, true
}

// parseCacheControl simples: converte para map[directive]value. Diretivas sem valor retornam "" e true.
func parseCacheControl(v string) map[string]string {
	m := make(map[string]string)
	if v == "" {
		return m
	}
	parts := strings.Split(v, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" { continue }
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

// buildCacheKey gera uma chave determinística.
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

// filterHeaders remove hop-by-hop e outros cabeçalhos que não devem ser reusados do cache.
func filterHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vv := range h {
		// remove hop-by-hop
		remove := false
		for _, hh := range hopHeaders {
			if strings.EqualFold(k, hh) {
				remove = true
				break
			}
		}
		if remove { continue }
		for _, v := range vv {
			out.Add(k, v)
		}
	}
	return out
}
