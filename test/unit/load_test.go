package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	proxy "traefik-challenge-2/internal/proxy"
)

func TestHighVolume(t *testing.T) {
	banner("load_test.go")

	// Upstream test server simulating processing latency.
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(200)
	}))
	t.Cleanup(upstreamServer.Close)

	// Reverse proxy configured with queueing and concurrency limits.
	targetURL, _ := url.Parse(upstreamServer.URL)
	reverseProxy := proxy.NewReverseProxy(targetURL, proxy.NewLRUCache(0), false).WithQueue(proxy.QueueConfig{
		// Allow up to 20 queued requests and 5 concurrent in-flight requests.
		MaxQueue:       20,
		MaxConcurrent:  5,
		// Fail fast if enqueue takes longer than 1s.
		EnqueueTimeout: time.Second,
	})
	// Disable health checks to avoid test flakiness.
	reverseProxy.SetHealthCheckEnabled(false)

	// Send a burst of requests to exercise the queue and backpressure behavior.
	const totalRequests = 200
	var requestWG sync.WaitGroup
	responseCodes := make(chan int, totalRequests)

	for i := 0; i < totalRequests; i++ {
		requestWG.Add(1)
		go func() {
			defer requestWG.Done()
			w := httptest.NewRecorder()
			reverseProxy.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			responseCodes <- w.Code
		}()
	}
	requestWG.Wait()
	close(responseCodes)

	// Tally results by HTTP status code.
	var successCount, rejectedCount, otherCount int
	for code := range responseCodes {
		switch code {
		case http.StatusOK:
			successCount++
		case http.StatusTooManyRequests:
			rejectedCount++
		default:
			otherCount++
		}
	}

	// Validate only expected statuses and ensure some success.
	if otherCount != 0 {
		t.Fatalf("unexpected statuses seen: %d", otherCount)
	}
	if successCount == 0 {
		t.Fatalf("no successful responses; expected some to pass through")
	}

	if successCount == 0 {
		t.Fatalf("no successful responses; expected some to pass through")
	}
}
