package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	proxy "traefik-challenge-2/internal/proxy"
)

// newProxy constructs a reverse proxy for tests with optional cache and queueing.
// Health checks are disabled unless overridden by the caller.
func newProxy(t *testing.T, targetURL *url.URL, cacheStore proxy.Cache, cacheEnabled bool, queueCfg *proxy.QueueConfig) http.Handler {
	t.Helper()
	rp := proxy.NewReverseProxy(targetURL, cacheStore, cacheEnabled)
	// Disable active health checks for deterministic tests.
	rp.SetHealthCheckEnabled(false)
	if queueCfg != nil {
		rp = rp.WithQueue(*queueCfg)
	}
	return rp
}

func TestCache_HitAndMiss(t *testing.T) {
	// Verifies a first MISS populates the cache and subsequent request is a HIT.
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(1024)
	proxyHandler := newProxy(t, targetURL, lruCache, true, nil)

	// First request should be a MISS and populate cache.
	firstReq := httptest.NewRequest(http.MethodGet, "/", nil)
	firstRec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", firstRec.Code)
	}
	if got := firstRec.Body.String(); got != "hello" {
		t.Fatalf("unexpected body: %q", got)
	}

	// Second request should be a HIT (no new upstream hit).
	secondReq := httptest.NewRequest(http.MethodGet, "/", nil)
	secondRec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(secondRec, secondReq)

	if got := atomic.LoadInt64(&upstreamHits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := secondRec.Result().Header.Get("X-Cache"); xc == "" {
		t.Errorf("expected X-Cache header to be set (HIT), got empty")
	}
}

func TestCache_RespectsNoCacheRequestDirective(t *testing.T) {
	// Ensures request header "Cache-Control: no-cache" bypasses the cache.
	banner("cache_test.go")
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = io.WriteString(w, "fresh")
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(1024)
	proxyHandler := newProxy(t, targetURL, lruCache, true, nil)

	// Warm cache with an initial GET.
	warmRec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(warmRec, httptest.NewRequest(http.MethodGet, "/", nil))

	// Bypass with Cache-Control: no-cache.
	bypassReq := httptest.NewRequest(http.MethodGet, "/", nil)
	bypassReq.Header.Set("Cache-Control", "no-cache")
	bypassRec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(bypassRec, bypassReq)

	if xc := bypassRec.Result().Header.Get("X-Cache"); xc == "HIT" {
		t.Errorf("unexpected X-Cache=HIT when request had no-cache")
	}
}

func TestCache_ExpiryAndRefetch(t *testing.T) {
	// Verifies entries expire after TTL and are refreshed on next access.
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=1")
		_, _ = w.Write([]byte("v" + strconv.FormatInt(upstreamHits, 10)))
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(1024)
	proxyHandler := newProxy(t, targetURL, lruCache, true, nil)

	// First request populates cache.
	rec1 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec1.Body.String(); got != "v1" {
		t.Fatalf("want v1, got %q", got)
	}

	// Hit before TTL.
	time.Sleep(200 * time.Millisecond)
	rec2 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec2.Body.String(); got != "v1" {
		t.Fatalf("want cached v1 before expiry, got %q", got)
	}

	// After TTL, should refetch.
	time.Sleep(1100 * time.Millisecond)
	rec3 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec3.Body.String(); got == "v1" {
		t.Fatalf("expected refreshed body after expiry, still got %q", got)
	}
}

func TestCache_POST_Hit(t *testing.T) {
	// Ensures identical POST requests can be cached and HIT on subsequent calls.
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=120")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("post-ok"))
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(256)
	proxyHandler := newProxy(t, targetURL, lruCache, true, nil)

	// First POST (MISS).
	postReq1 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("alpha"))
	postRec1 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(postRec1, postReq1)
	if postRec1.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", postRec1.Code)
	}

	// Second identical POST (HIT).
	postReq2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("alpha"))
	postRec2 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(postRec2, postReq2)

	if got := atomic.LoadInt64(&upstreamHits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := postRec2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected X-Cache=HIT, got %q", xc)
	}
}

func TestCache_POST_DifferentBodies_NotHit(t *testing.T) {
	// POST requests with different bodies should not HIT the same cache entry.
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("post-" + r.Method))
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(256)
	proxyHandler := newProxy(t, targetURL, lruCache, true, nil)

	// Body "alpha".
	postAlphaReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("alpha"))
	postAlphaRec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(postAlphaRec, postAlphaReq)

	// Body "beta" -> should not HIT alpha entry (hash differs).
	postBetaReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("beta"))
	postBetaRec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(postBetaRec, postBetaReq)

	if got := atomic.LoadInt64(&upstreamHits); got != 2 {
		t.Fatalf("expected 2 upstream hits (different bodies), got %d", got)
	}
	if xc := postBetaRec.Result().Header.Get("X-Cache"); xc == "HIT" {
		t.Fatalf("did not expect HIT for different body, got %q", xc)
	}
}

func TestCache_PUT_Hit(t *testing.T) {
	// PUT requests to same resource should HIT when cacheable.
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=90")
		_, _ = w.Write([]byte("put"))
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(64)
	proxyHandler := newProxy(t, targetURL, lruCache, true, nil)

	putReq1 := httptest.NewRequest(http.MethodPut, "/res", nil)
	putRec1 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(putRec1, putReq1)

	putReq2 := httptest.NewRequest(http.MethodPut, "/res", nil)
	putRec2 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(putRec2, putReq2)

	if got := atomic.LoadInt64(&upstreamHits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := putRec2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected HIT for second PUT, got %q", xc)
	}
}

func TestCache_PATCH_Hit(t *testing.T) {
	// PATCH requests with identical body and path should HIT.
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=45")
		_, _ = w.Write([]byte("patch"))
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(64)
	proxyHandler := newProxy(t, targetURL, lruCache, true, nil)

	patchReq1 := httptest.NewRequest(http.MethodPatch, "/item/1", strings.NewReader("{}"))
	patchRec1 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(patchRec1, patchReq1)

	patchReq2 := httptest.NewRequest(http.MethodPatch, "/item/1", strings.NewReader("{}"))
	patchRec2 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(patchRec2, patchReq2)

	if got := atomic.LoadInt64(&upstreamHits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := patchRec2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected HIT for second PATCH, got %q", xc)
	}
}

func TestCache_DELETE_Hit(t *testing.T) {
	// DELETE requests to same resource should HIT when cacheable.
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(64)
	proxyHandler := newProxy(t, targetURL, lruCache, true, nil)

	delReq1 := httptest.NewRequest(http.MethodDelete, "/thing", nil)
	delRec1 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(delRec1, delReq1)

	delReq2 := httptest.NewRequest(http.MethodDelete, "/thing", nil)
	delRec2 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(delRec2, delReq2)

	if got := atomic.LoadInt64(&upstreamHits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := delRec2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected HIT for second DELETE, got %q", xc)
	}
}

func TestCache_HEAD_Hit(t *testing.T) {
	// HEAD requests should HIT and not require a body.
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=25")
		// No body for HEAD.
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(64)
	proxyHandler := newProxy(t, targetURL, lruCache, true, nil)

	headReq1 := httptest.NewRequest(http.MethodHead, "/probe", nil)
	headRec1 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(headRec1, headReq1)

	headReq2 := httptest.NewRequest(http.MethodHead, "/probe", nil)
	headRec2 := httptest.NewRecorder()
	proxyHandler.ServeHTTP(headRec2, headReq2)

	if got := atomic.LoadInt64(&upstreamHits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := headRec2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected HIT for second HEAD, got %q", xc)
	}
}

func TestDisallowedMethod_NoCacheInteraction(t *testing.T) {
	// When method is not allowed, proxy should return 405 without calling upstream or cache.
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(128)

	reverseProxy := proxy.NewReverseProxy(targetURL, lruCache, true)
	reverseProxy.SetAllowedMethods([]string{"GET"}) // Only GET allowed.

	disallowedReq := httptest.NewRequest(http.MethodPost, "/x", nil)
	disallowedRec := httptest.NewRecorder()
	reverseProxy.ServeHTTP(disallowedRec, disallowedReq)

	if disallowedRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", disallowedRec.Code)
	}
	if atomic.LoadInt64(&upstreamHits) != 0 {
		t.Fatalf("upstream should not have been called for disallowed method")
	}
	if v := disallowedRec.Header().Get("X-Cache"); v != "" {
		t.Fatalf("did not expect X-Cache header on disallowed method, got %q", v)
	}
	if allow := disallowedRec.Header().Get("Allow"); allow != "GET" {
		t.Fatalf("expected Allow header with GET, got %q", allow)
	}
}

// Ensures allowed method still leverages cache (MISS then HIT) under method restriction.
func TestAllowedMethod_CacheWorksWithRestriction(t *testing.T) {
	banner("cache_test.go")
	var upstreamHits int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=120")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstreamServer.Close)

	targetURL, _ := url.Parse(upstreamServer.URL)
	lruCache := proxy.NewLRUCache(128)

	reverseProxy := proxy.NewReverseProxy(targetURL, lruCache, true)
	// Disable active health checks for deterministic tests.
	reverseProxy.SetHealthCheckEnabled(false)
	reverseProxy.SetAllowedMethods([]string{"GET"}) // Only GET allowed.

	// First GET (MISS).
	getReq1 := httptest.NewRequest(http.MethodGet, "/", nil)
	getRec1 := httptest.NewRecorder()
	reverseProxy.ServeHTTP(getRec1, getReq1)
	if getRec1.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", getRec1.Code)
	}
	if xc := getRec1.Header().Get("X-Cache"); xc != "MISS" {
		t.Fatalf("expected X-Cache=MISS, got %q", xc)
	}

	// Second GET (HIT).
	getReq2 := httptest.NewRequest(http.MethodGet, "/", nil)
	getRec2 := httptest.NewRecorder()
	reverseProxy.ServeHTTP(getRec2, getReq2)
	if getRec2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", getRec2.Code)
	}
	if xc := getRec2.Header().Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected X-Cache=HIT, got %q", xc)
	}
	if atomic.LoadInt64(&upstreamHits) != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", upstreamHits)
	}
}
