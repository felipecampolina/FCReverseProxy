package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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