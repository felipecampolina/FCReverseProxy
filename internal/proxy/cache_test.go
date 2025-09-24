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

	"traefik-challenge-2/internal/proxy"
)

func newProxy(t *testing.T, target *url.URL, cache proxy.Cache, cacheOn bool, qcfg *proxy.QueueConfig) http.Handler {
	t.Helper()
	rp := proxy.NewReverseProxy(target, cache, cacheOn)
	if qcfg != nil {
		rp = rp.WithQueue(*qcfg)
	}
	return rp
}

func TestCache_HitAndMiss(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(1024)
	h := newProxy(t, tgt, cache, true, nil)

	// First request should be a MISS and populate cache
	r1 := httptest.NewRequest("GET", "/", nil)
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)
	if w1.Code != 200 {
		t.Fatalf("want 200, got %d", w1.Code)
	}
	if got := w1.Body.String(); got != "hello" {
		t.Fatalf("unexpected body: %q", got)
	}

	// Second request should be a HIT (no new upstream hit)
	r2 := httptest.NewRequest("GET", "/", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)

	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := w2.Result().Header.Get("X-Cache"); xc == "" {
		t.Errorf("expected X-Cache header to be set (HIT), got empty")
	}
}

func TestCache_RespectsNoCacheRequestDirective(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = io.WriteString(w, "fresh")
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(1024)
	h := newProxy(t, tgt, cache, true, nil)

	// Warm cache
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, httptest.NewRequest("GET", "/", nil))

	// Bypass with Cache-Control: no-cache
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("Cache-Control", "no-cache")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)

	if xc := w2.Result().Header.Get("X-Cache"); xc == "HIT" {
		t.Errorf("unexpected X-Cache=HIT when request had no-cache")
	}
}

func TestCache_ExpiryAndRefetch(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Cache-Control", "public, max-age=1")
		_, _ = w.Write([]byte("v" + strconv.FormatInt(hits, 10)))
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(1024)
	h := newProxy(t, tgt, cache, true, nil)

	// First request
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, httptest.NewRequest("GET", "/", nil))
	if got := w1.Body.String(); got != "v1" {
		t.Fatalf("want v1, got %q", got)
	}

	// Hit before TTL
	time.Sleep(200 * time.Millisecond)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest("GET", "/", nil))
	if got := w2.Body.String(); got != "v1" {
		t.Fatalf("want cached v1 before expiry, got %q", got)
	}

	// After TTL, should refetch
	time.Sleep(1100 * time.Millisecond)
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, httptest.NewRequest("GET", "/", nil))
	if got := w3.Body.String(); got == "v1" {
		t.Fatalf("expected refreshed body after expiry, still got %q", got)
	}
}

func TestCache_POST_Hit(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Cache-Control", "public, max-age=120")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("post-ok"))
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(256)
	h := newProxy(t, tgt, cache, true, nil)

	// First POST (MISS)
	r1 := httptest.NewRequest("POST", "/", strings.NewReader("alpha"))
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)
	if w1.Code != 200 {
		t.Fatalf("want 200, got %d", w1.Code)
	}

	// Second identical POST (HIT)
	r2 := httptest.NewRequest("POST", "/", strings.NewReader("alpha"))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)

	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := w2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected X-Cache=HIT, got %q", xc)
	}
}

func TestCache_POST_DifferentBodies_NotHit(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("post-" + r.Method))
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(256)
	h := newProxy(t, tgt, cache, true, nil)

	// Body alpha
	r1 := httptest.NewRequest("POST", "/", strings.NewReader("alpha"))
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)

	// Body beta -> should not HIT alpha entry (hash differs)
	r2 := httptest.NewRequest("POST", "/", strings.NewReader("beta"))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)

	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Fatalf("expected 2 upstream hits (different bodies), got %d", got)
	}
	if xc := w2.Result().Header.Get("X-Cache"); xc == "HIT" {
		t.Fatalf("did not expect HIT for different body, got %q", xc)
	}
}

func TestCache_PUT_Hit(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Cache-Control", "public, max-age=90")
		_, _ = w.Write([]byte("put"))
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(64)
	h := newProxy(t, tgt, cache, true, nil)

	r1 := httptest.NewRequest("PUT", "/res", nil)
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)

	r2 := httptest.NewRequest("PUT", "/res", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)

	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := w2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected HIT for second PUT, got %q", xc)
	}
}

func TestCache_PATCH_Hit(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Cache-Control", "public, max-age=45")
		_, _ = w.Write([]byte("patch"))
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(64)
	h := newProxy(t, tgt, cache, true, nil)

	r1 := httptest.NewRequest("PATCH", "/item/1", strings.NewReader("{}"))
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)

	r2 := httptest.NewRequest("PATCH", "/item/1", strings.NewReader("{}"))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)

	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := w2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected HIT for second PATCH, got %q", xc)
	}
}

func TestCache_DELETE_Hit(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Cache-Control", "public, max-age=30")
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(64)
	h := newProxy(t, tgt, cache, true, nil)

	r1 := httptest.NewRequest("DELETE", "/thing", nil)
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)

	r2 := httptest.NewRequest("DELETE", "/thing", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)

	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := w2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected HIT for second DELETE, got %q", xc)
	}
}

func TestCache_HEAD_Hit(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Cache-Control", "public, max-age=25")
		// No body for HEAD
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(64)
	h := newProxy(t, tgt, cache, true, nil)

	r1 := httptest.NewRequest("HEAD", "/probe", nil)
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)

	r2 := httptest.NewRequest("HEAD", "/probe", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)

	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
	if xc := w2.Result().Header.Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected HIT for second HEAD, got %q", xc)
	}
}

func TestDisallowedMethod_NoCacheInteraction(t *testing.T) {
	var upstreamHits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	u, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(128)

	rp := proxy.NewReverseProxy(u, cache, true)
	rp.SetAllowedMethods([]string{"GET"}) // Only GET allowed

	req := httptest.NewRequest("POST", "/x", nil)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
	if atomic.LoadInt64(&upstreamHits) != 0 {
		t.Fatalf("upstream should not have been called for disallowed method")
	}
	if v := w.Header().Get("X-Cache"); v != "" {
		t.Fatalf("did not expect X-Cache header on disallowed method, got %q", v)
	}
	if allow := w.Header().Get("Allow"); allow != "GET" {
		t.Fatalf("expected Allow header with GET, got %q", allow)
	}
}

// Ensures allowed method still leverages cache (MISS then HIT) under method restriction.
func TestAllowedMethod_CacheWorksWithRestriction(t *testing.T) {
	var upstreamHits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Cache-Control", "public, max-age=120")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(up.Close)

	u, _ := url.Parse(up.URL)
	cache := proxy.NewLRUCache(128)

	rp := proxy.NewReverseProxy(u, cache, true)
	rp.SetAllowedMethods([]string{"GET"}) // Only GET allowed

	// First GET (MISS)
	r1 := httptest.NewRequest("GET", "/", nil)
	w1 := httptest.NewRecorder()
	rp.ServeHTTP(w1, r1)
	if w1.Code != 200 {
		t.Fatalf("want 200, got %d", w1.Code)
	}
	if xc := w1.Header().Get("X-Cache"); xc != "MISS" {
		t.Fatalf("expected X-Cache=MISS, got %q", xc)
	}

	// Second GET (HIT)
	r2 := httptest.NewRequest("GET", "/", nil)
	w2 := httptest.NewRecorder()
	rp.ServeHTTP(w2, r2)
	if w2.Code != 200 {
		t.Fatalf("want 200, got %d", w2.Code)
	}
	if xc := w2.Header().Get("X-Cache"); xc != "HIT" {
		t.Fatalf("expected X-Cache=HIT, got %q", xc)
	}
	if atomic.LoadInt64(&upstreamHits) != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", upstreamHits)
	}
}
