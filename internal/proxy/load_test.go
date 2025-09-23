package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"traefik-challenge-2/internal/proxy"
)

// This is a stress-style test to exercise high volume under queueing.
func TestHighVolume(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	tgt, _ := url.Parse(up.URL)
	rp := proxy.NewReverseProxy(tgt, proxy.NewLRUCache(0), false).WithQueue(proxy.QueueConfig{
		MaxQueue:       20,
		MaxConcurrent:  5,
		EnqueueTimeout: time.Second,
	})

	const N = 200
	var wg sync.WaitGroup
	codes := make(chan int, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			rp.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)

	var ok, rejected, other int
	for c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			rejected++
		default:
			other++
		}
	}

	if other != 0 {
		t.Fatalf("unexpected statuses seen: %d", other)
	}
	if ok == 0 {
		t.Fatalf("no successful responses; expected some to pass through")
	}
}
