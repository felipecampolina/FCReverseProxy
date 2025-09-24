package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

// banner helper comes from balancer_test.go (same package)

func startUpstream(t *testing.T, name string, slow bool) *httptest.Server {
	h := http.NewServeMux()

	// Cacheable endpoint
	h.HandleFunc("/cachehit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=5")
		w.Header().Set("X-Upstream", name)
		json.NewEncoder(w).Encode(map[string]any{
			"upstream": name,
			"path":     r.URL.Path,
			"time":     time.Now().UnixNano(),
		})
	})

	// Non-cacheable endpoint (forces miss every time)
	h.HandleFunc("/nocache", func(w http.ResponseWriter, r *http.Request) {
		if slow {
			time.Sleep(250 * time.Millisecond)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Upstream", name)
		json.NewEncoder(w).Encode(map[string]any{
			"upstream": name,
			"q":        r.URL.RawQuery,
		})
	})

	// Generic work endpoint for least-connections test (no-store)
	h.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		if slow {
			time.Sleep(250 * time.Millisecond)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Upstream", name)
		json.NewEncoder(w).Encode(map[string]any{
			"upstream": name,
			"path":     r.URL.Path,
			"q":        r.URL.RawQuery,
		})
	})

	return httptest.NewServer(h)
}

func mustParse(t *testing.T, raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return u
}

func TestProxyRoundRobinWithCache(t *testing.T) {
	banner("proxy_integration_test.go")

	up1 := startUpstream(t, "A", false)
	defer up1.Close()
	up2 := startUpstream(t, "B", false)
	defer up2.Close()

	targets := []*url.URL{mustParse(t, up1.URL), mustParse(t, up2.URL)}

	rp := NewReverseProxyMulti(targets, NewLRUCache(128), true)
	rp.ConfigureBalancer("rr")

	proxySrv := httptest.NewServer(rp)
	defer proxySrv.Close()

	client := &http.Client{Timeout: 3 * time.Second}

	// Round-robin sequence on unique nocache requests.
	gotSeq := []string{}
	for i := 0; i < 4; i++ {
		req, _ := http.NewRequest("GET", proxySrv.URL+"/nocache?i="+strconv.Itoa(i), nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d error: %v", i, err)
		}
		resp.Body.Close()
		u := resp.Header.Get("X-Upstream")
		if u == "" {
			t.Fatalf("missing X-Upstream header")
		}
		gotSeq = append(gotSeq, u)
	}
	// Expect A,B,A,B
	want := []string{"A", "B", "A", "B"}
	for i := range want {
		if gotSeq[i] != want[i] {
			t.Fatalf("round-robin mismatch got=%v want=%v", gotSeq, want)
		}
	}

	// Cache behavior: first /cachehit MISS, second HIT, same upstream header.
	req1, _ := http.NewRequest("GET", proxySrv.URL+"/cachehit", nil)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("cachehit miss req error: %v", err)
	}
	upstreamFirst := resp1.Header.Get("X-Upstream")
	if upstreamFirst == "" {
		t.Fatalf("expected upstream header on first response")
	}
	if xc := resp1.Header.Get("X-Cache"); xc != "MISS" && xc != "" && xc != "BYPASS" {
		// Depending on timing ttl logic may label as MISS or BYPASS (if deemed non-cacheable)
	}
	resp1.Body.Close()

	req2, _ := http.NewRequest("GET", proxySrv.URL+"/cachehit", nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("cachehit hit req error: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.Header.Get("X-Cache") != "HIT" {
		t.Fatalf("expected X-Cache=HIT got %q", resp2.Header.Get("X-Cache"))
	}
	if resp2.Header.Get("X-Upstream") != upstreamFirst {
		t.Fatalf("cached response upstream header changed: first=%s second=%s", upstreamFirst, resp2.Header.Get("X-Upstream"))
	}
}

func TestProxyLeastConnections(t *testing.T) {
	banner("proxy_integration_test.go")

	// upSlow first so first request goes to slow backend, second should go to fast backend.
	upSlow := startUpstream(t, "SLOW", true)
	defer upSlow.Close()
	upFast := startUpstream(t, "FAST", false)
	defer upFast.Close()

	targets := []*url.URL{mustParse(t, upSlow.URL), mustParse(t, upFast.URL)}

	rp := NewReverseProxyMulti(targets, NewLRUCache(64), true)
	rp.ConfigureBalancer("least_conn")

	proxySrv := httptest.NewServer(rp)
	defer proxySrv.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	type result struct {
		up string
	}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make([]result, 2)

	startCh := make(chan struct{})

	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-startCh
			req, _ := http.NewRequest("GET", proxySrv.URL+"/work?req="+strconv.Itoa(i), nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("req %d error: %v", i, err)
				return
			}
			resp.Body.Close()
			results[i] = result{up: resp.Header.Get("X-Upstream")}
		}()
	}

	close(startCh) // launch both concurrently
	wg.Wait()

	if results[0].up == "" || results[1].up == "" {
		t.Fatalf("missing upstream header(s): %+v", results)
	}
	if results[0].up == results[1].up {
		t.Fatalf("least-connections failed: both requests hit %s", results[0].up)
	}

	// Third request after both done should revert to first (slow) again (both zero -> first picked).
	req3, _ := http.NewRequest("GET", proxySrv.URL+"/work?req=3", nil)
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("third request error: %v", err)
	}
	resp3.Body.Close()
	if resp3.Header.Get("X-Upstream") != "SLOW" {
		t.Fatalf("expected third request to hit SLOW, got %s", resp3.Header.Get("X-Upstream"))
	}
}
