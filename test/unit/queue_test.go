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

	// Track live concurrency and the highest concurrency observed.
	var currentConcurrency int64
	var peakConcurrency int64

	// Upstream handler simulates work and records concurrency safely.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Increment in-flight counter and capture current value.
		cur := atomic.AddInt64(&currentConcurrency, 1)

		// Update the peak if this goroutine raises it.
		for {
			observedPeak := atomic.LoadInt64(&peakConcurrency)
			if cur <= observedPeak || atomic.CompareAndSwapInt64(&peakConcurrency, observedPeak, cur) {
				break
			}
		}

		// Simulate upstream latency and then decrement.
		time.Sleep(200 * time.Millisecond)
		atomic.AddInt64(&currentConcurrency, -1)
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(upstream.Close)

	targetURL, _ := url.Parse(upstream.URL)

	// Reverse proxy with a queue: 1 concurrent, buffer 2, then 429s.
	reverseProxy := proxy.NewReverseProxy(targetURL, proxy.NewLRUCache(0), false)
	reverseProxy = reverseProxy.WithQueue(proxy.QueueConfig{
		MaxQueue:        2,
		MaxConcurrent:   1,
		EnqueueTimeout:  time.Second,
		QueueWaitHeader: true,
	})
	// Disable background health checks for deterministic tests.
	reverseProxy.SetHealthCheckEnabled(false)

	handler := reverseProxy

	var wg sync.WaitGroup
	requestCount := 5 // 1 active + 2 queued + 2 rejected (429)
	statusCodes := make([]int, requestCount)

	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			statusCodes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	var okCount, rejectedCount int
	for _, status := range statusCodes {
		switch status {
		case http.StatusOK:
			okCount++
		case http.StatusTooManyRequests:
			rejectedCount++
		default:
			t.Fatalf("unexpected status %d", status)
		}
	}

	// Expect 1 active + 2 queued to succeed.
	if okCount != 3 {
		t.Fatalf("expected 3 OK responses, got %d (codes=%v)", okCount, statusCodes)
	}
	// Expect the overflow to be rejected with 429.
	if rejectedCount != 2 {
		t.Fatalf("expected 2 rejections with 429, got %d (codes=%v)", rejectedCount, statusCodes)
	}
	// Concurrency must never exceed the configured limit.
	if peakConcurrency > 1 {
		t.Fatalf("concurrency exceeded limit: peak=%d", peakConcurrency)
	}
}

func TestQueue_TimeoutWhileWaiting(t *testing.T) {
	banner("queue_test.go")

	// Signal when the first request actually reaches upstream.
	firstRequestStarted := make(chan struct{})
	var startOnce sync.Once

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(firstRequestStarted) })
		time.Sleep(2 * time.Second) // hold the only active slot
		w.WriteHeader(200)
	}))
	t.Cleanup(upstream.Close)

	targetURL, _ := url.Parse(upstream.URL)
	reverseProxy := proxy.NewReverseProxy(targetURL, proxy.NewLRUCache(0), false).WithQueue(proxy.QueueConfig{
		MaxQueue:       1,
		MaxConcurrent:  1,
		EnqueueTimeout: 10 * time.Millisecond, // force quick timeout for the queued request
	})
	reverseProxy.SetHealthCheckEnabled(false)

	// First request occupies the only active slot.
	go func() {
		rec := httptest.NewRecorder()
		reverseProxy.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	}()

	// Wait until the first request is definitely active.
	<-firstRequestStarted

	// Second request should time out while queued.
	recSecond := httptest.NewRecorder()
	reverseProxy.ServeHTTP(recSecond, httptest.NewRequest("GET", "/", nil))
	if recSecond.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for queue wait timeout, got %d", recSecond.Code)
	}
}

func TestQueue_ClientCancellationWhileQueued(t *testing.T) {
	banner("queue_test.go")

	// Signal when the first request actually reaches upstream.
	firstRequestStarted := make(chan struct{})
	var startOnce sync.Once

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(firstRequestStarted) })
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(200)
	}))
	t.Cleanup(upstream.Close)

	targetURL, _ := url.Parse(upstream.URL)
	reverseProxy := proxy.NewReverseProxy(targetURL, proxy.NewLRUCache(0), false).WithQueue(proxy.QueueConfig{
		MaxQueue:       1,
		MaxConcurrent:  1,
		EnqueueTimeout: time.Second,
	})
	reverseProxy.SetHealthCheckEnabled(false)

	// Fill active slot with the first request.
	go func() {
		rec := httptest.NewRecorder()
		reverseProxy.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	}()

	// Ensure the first request is actually active.
	<-firstRequestStarted

	// Second request cancels while queued.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	queuedReq := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	reverseProxy.ServeHTTP(rec, queuedReq)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for client cancellation, got %d", rec.Code)
	}
}
