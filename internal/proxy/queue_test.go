package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"traefik-challenge-2/internal/proxy"
)

func TestQueue_ConcurrencyLimitAndQueueing(t *testing.T) {
	banner("queue_test.go")
	var concurrent int64
	var peak int64
	// upstream that sleeps and tracks concurrency
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := atomic.AddInt64(&concurrent, 1)
		for {
			p := atomic.LoadInt64(&peak)
			if c <= p || atomic.CompareAndSwapInt64(&peak, p, c) {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
		atomic.AddInt64(&concurrent, -1)
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	rp := proxy.NewReverseProxy(tgt, proxy.NewLRUCache(0), false)
	rp = rp.WithQueue(proxy.QueueConfig{
		MaxQueue:        2,
		MaxConcurrent:   1,
		EnqueueTimeout:  time.Second,
		QueueWaitHeader: true,
	})
	// Disable health checks for unit tests
	rp.SetHealthCheckEnabled(false)

	h := rp

	var wg sync.WaitGroup
	count := 5 // 1 active + 2 queued + 2 rejected
	codes := make([]int, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			codes[i] = w.Code
		}(i)
	}
	wg.Wait()

	var ok, rejected int
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			rejected++
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}

	if ok != 3 { // 1 active + 2 queued
		t.Fatalf("expected 3 OK responses, got %d (codes=%v)", ok, codes)
	}
	if rejected != 2 {
		t.Fatalf("expected 2 rejections with 429, got %d (codes=%v)", rejected, codes)
	}
	if peak > 1 {
		t.Fatalf("concurrency exceeded limit: peak=%d", peak)
	}
}

func TestQueue_TimeoutWhileWaiting(t *testing.T) {
	banner("queue_test.go")
	started := make(chan struct{})
	var once sync.Once

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Signal when the first request actually hits upstream (i.e., holds active)
		once.Do(func() { close(started) })
		time.Sleep(2 * time.Second)
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	rp := proxy.NewReverseProxy(tgt, proxy.NewLRUCache(0), false).WithQueue(proxy.QueueConfig{
		MaxQueue:       1,
		MaxConcurrent:  1,
		EnqueueTimeout: 10 * time.Millisecond,
	})
	// Disable health checks for unit tests
	rp.SetHealthCheckEnabled(false)

	// First request occupies the only active slot
	go func() {
		w := httptest.NewRecorder()
		rp.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	}()

	// Wait until the first request is definitely active
	<-started

	// Second request should time out while queued
	w2 := httptest.NewRecorder()
	rp.ServeHTTP(w2, httptest.NewRequest("GET", "/", nil))
	if w2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for queue wait timeout, got %d", w2.Code)
	}
}

func TestQueue_ClientCancellationWhileQueued(t *testing.T) {
	banner("queue_test.go")

	started := make(chan struct{})
	var once sync.Once

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	rp := proxy.NewReverseProxy(tgt, proxy.NewLRUCache(0), false).WithQueue(proxy.QueueConfig{
		MaxQueue:       1,
		MaxConcurrent:  1,
		EnqueueTimeout: time.Second,
	})
	// Disable health checks for unit tests
	rp.SetHealthCheckEnabled(false)

	// Fill active slot
	go func() {
		w := httptest.NewRecorder()
		rp.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	}()

	// Ensure the first request is actually active
	<-started

	// Second request cancels while queued
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for client cancellation, got %d", w.Code)
	}
}
