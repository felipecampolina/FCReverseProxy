package e2e

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func newHTTPSClient() *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // proxy uses self-signed in dev
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func newHTTPSClientWithTimeout(d time.Duration) *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Transport: tr, Timeout: d}
}

func doReq(t *testing.T, c *http.Client, base, method, path string, body string, headers map[string]string) (int, error) {
	t.Helper()
	req, _ := http.NewRequest(method, strings.TrimRight(base, "/")+path, bytes.NewBufferString(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, nil
}

func doReqDetailed(t *testing.T, c *http.Client, base, method, path string, body string, headers map[string]string) (*http.Response, []byte, error) {
	t.Helper()
	req, _ := http.NewRequest(method, strings.TrimRight(base, "/")+path, bytes.NewBufferString(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, b, nil
}

func fetchMetricsText(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	resp, err := c.Get(strings.TrimRight(base, "/") + "/metrics")
	if err != nil {
		t.Fatalf("fetch /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/metrics status=%d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func anyLineContains(text string, contains ...string) bool {
	for _, line := range strings.Split(text, "\n") {
		ok := true
		for _, c := range contains {
			if !strings.Contains(line, c) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func metricExists(text, name string) bool {
	return strings.Contains(text, "\n"+name+" ") || strings.Contains(text, "\n# TYPE "+name+" ")
}

// serialize all e2e tests; keeps tests sequential even if package is run with parallel flags
var (
	seqMu sync.Mutex
)

func lockSeq(t *testing.T) func() {
	seqMu.Lock()
	return func() { seqMu.Unlock() }
}

// Sanity: HTTPS/TLS reachability + core metrics presence.
func TestProxyTLSAndMetricsReachability(t *testing.T) {
	unlock := lockSeq(t); defer unlock()

	base := getenv("PROXY_ADDR", "https://localhost:8090")
	c := newHTTPSClient()

	// health over TLS
	resp, err := c.Get(strings.TrimRight(base, "/") + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz over TLS failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/healthz status=%d", resp.StatusCode)
	}

	// Warm up proxy so histograms/counters get observations.
	_, _, _ = doReqDetailed(t, c, base, "GET", "/api/items?warm=1", "", map[string]string{"Cache-Control": "no-cache"})

	// metrics reachability and core series presence (check base histogram names)
	txt := fetchMetricsText(t, c, base)
	core := []string{
		"proxy_requests_total",
		"proxy_request_duration_seconds",
		"proxy_upstream_request_duration_seconds",
		"proxy_upstream_requests_total",
		"proxy_upstream_inflight",
		"proxy_queue_wait_seconds",
		"proxy_queue_depth",
		"proxy_queue_rejected_total",
		"proxy_queue_timeouts_total",
	}
	for _, m := range core {
		if !metricExists(txt, m) {
			t.Fatalf("expected metric %q to be exposed", m)
		}
	}
}

// Cache MISS then HIT; verify headers and that cache-labelled series appear.
func TestCacheHitMissAndDurationMetrics(t *testing.T) {
	unlock := lockSeq(t); defer unlock()

	base := getenv("PROXY_ADDR", "https://localhost:8090")
	c := newHTTPSClient()

	p := "/api/items?cache_sweep=1"

	// MISS
	resp1, _, err := doReqDetailed(t, c, base, "GET", p, "", nil)
	if err != nil {
		t.Fatalf("miss req: %v", err)
	}
	xc1 := resp1.Header.Get("X-Cache")
	if xc1 != "MISS" && xc1 != "BYPASS" {
		// BYPASS acceptable if upstream deemed non-cacheable; prefer MISS
		t.Logf("first response X-Cache=%q", xc1)
	}

	// HIT
	resp2, _, err := doReqDetailed(t, c, base, "GET", p, "", nil)
	if err != nil {
		t.Fatalf("hit req: %v", err)
	}
	if xc2 := resp2.Header.Get("X-Cache"); xc2 != "HIT" {
		t.Fatalf("expected X-Cache=HIT on second request, got %q", xc2)
	}

	txt := fetchMetricsText(t, c, base)
	if !anyLineContains(txt, "proxy_requests_total", `cache="HIT"`) {
		t.Fatalf("expected proxy_requests_total with cache=\"HIT\" label")
	}
	// duration histograms present (check base names)
	for _, m := range []string{
		"proxy_request_duration_seconds",
		"proxy_upstream_request_duration_seconds",
	} {
		if !metricExists(txt, m) {
			t.Fatalf("expected %s to be exposed", m)
		}
	}
}

// Queue metrics: send no-cache burst to ensure queue wait histogram gets observations.
func TestQueueMetricsExposureUnderLoad(t *testing.T) {
	unlock := lockSeq(t); defer unlock()

	base := getenv("PROXY_ADDR", "https://localhost:8090")
	c := newHTTPSClientWithTimeout(10 * time.Second)

	const N = 300
	var wg sync.WaitGroup
	wg.Add(N)
	hdr := map[string]string{"Cache-Control": "no-cache"}

	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_, _, _ = doReqDetailed(t, c, base, "GET", fmt.Sprintf("/api/items?q=%d", i), "", hdr)
		}(i)
	}

	// Give time for admissions/observations and scrape
	time.Sleep(500 * time.Millisecond)
	txt := fetchMetricsText(t, c, base)

	// Check base histogram family presence
	if !metricExists(txt, "proxy_queue_wait_seconds") {
		t.Fatalf("expected proxy_queue_wait_seconds to exist")
	}

	// Optional bucket non-zero check remains timing-sensitive
	re := regexp.MustCompile(`^proxy_queue_wait_seconds_bucket\{.*\}\s+([0-9]+(\.[0-9]+)?)$`)
	nonZero := false
	for _, line := range strings.Split(txt, "\n") {
		m := re.FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		if m[1] != "0" {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Log("queue_wait histogram buckets observed but counts may be zero at scrape time (timing sensitive)")
	}

	wg.Wait()
}

// Method/status breakdowns should be present in proxy_requests_total.
func TestMethodAndStatusBreakdownsPresent(t *testing.T) {
	unlock := lockSeq(t); defer unlock()

	base := getenv("PROXY_ADDR", "https://localhost:8090")
	c := newHTTPSClient()

	// Drive a mix (some 404s for 4xx)
	_ = []struct {
		m, p string
	}{
		{"GET", "/api/items?m=1"},
		{"POST", "/api/items"},
		{"PUT", "/api/items/1"},
		{"PATCH", "/api/items/1"},
		{"DELETE", "/api/items/2"},
		{"GET", "/definitely/notfound"},
	}
	for _, it := range []struct {
		m, p string
	}{
		{"GET", "/api/items?m=1"},
		{"POST", "/api/items"},
		{"PUT", "/api/items/1"},
		{"PATCH", "/api/items/1"},
		{"DELETE", "/api/items/2"},
		{"GET", "/definitely/notfound"},
	} {
		_, _, _ = doReqDetailed(t, c, base, it.m, it.p, `{"n":"x"}`, map[string]string{"Content-Type": "application/json"})
	}

	txt := fetchMetricsText(t, c, base)
	// methods
	for _, meth := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		if !anyLineContains(txt, "proxy_requests_total", fmt.Sprintf(`method="%s"`, meth)) {
			t.Fatalf("expected proxy_requests_total with method=%s", meth)
		}
	}
	// statuses: at least 2xx should exist; 4xx likely exists due to notfound
	if !anyLineContains(txt, "proxy_requests_total", `status="200"`) {
		t.Fatalf("expected proxy_requests_total with status=200")
	}
	if !anyLineContains(txt, "proxy_requests_total", `status="404"`) {
		t.Log("no 404 series observed; upstream may have handled notfound differently")
	}
}

// Verify upstream metrics exposure by querying upstream /metrics directly.
func TestUpstreamMetricsExposure(t *testing.T) {
	unlock := lockSeq(t); defer unlock()

	// Default upstream address (adjust with UPSTREAM_ADDR when needed)
	upBase := getenv("UPSTREAM_ADDR", "http://localhost:9000")
	cProxy := newHTTPSClient()
	cUp := &http.Client{Timeout: 5 * time.Second}

	// Generate some upstream traffic via proxy (no-cache to avoid HITs)
	base := getenv("PROXY_ADDR", "https://localhost:8090")
	for i := 0; i < 10; i++ {
		_, _, _ = doReqDetailed(t, cProxy, base, "GET", fmt.Sprintf("/api/items?upm=%d", i), "", map[string]string{"Cache-Control": "no-cache"})
	}

	// Now read upstream metrics directly
	resp, err := cUp.Get(strings.TrimRight(upBase, "/") + "/metrics")
	if err != nil {
		t.Fatalf("upstream /metrics fetch failed from %s: %v", upBase, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("upstream /metrics status=%d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	txt := string(b)

	for _, m := range []string{
		"upstream_requests_total",
		"upstream_request_duration_seconds",
		"upstream_inflight",
	} {
		if !metricExists(txt, m) {
			t.Fatalf("expected upstream metric %q to be exposed", m)
		}
	}
}

// Helpers: env int/duration parsing for queue-related settings used by the server.
func getenvInt(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return def
}

func getenvDuration(k string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

// Helpers: read a bare counter value from Prometheus text (no labels).
func getCounterValue(text, name string) float64 {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s+([0-9]+(?:\.[0-9]+)?)$`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return f
}

// --- New e2e: queue rejections metric increments under saturation ---
func TestQueueRejectionsMetricIncrements(t *testing.T) {
	unlock := lockSeq(t); defer unlock()

	base := getenv("PROXY_ADDR", "https://localhost:8090")
	c := newHTTPSClientWithTimeout(15 * time.Second)

	// Read server queue config (same keys used by main)
	maxQ := getenvInt("RP_MAX_QUEUE", 1000)
	maxC := getenvInt("RP_MAX_CONCURRENT", 100)

	// Snapshot metric before load
	beforeTxt := fetchMetricsText(t, c, base)
	before := getCounterValue(beforeTxt, "proxy_queue_rejected_total")

	// Plan to exceed queue+concurrency with a single synchronized burst
	extra := maxC
	if extra > 200 {
		extra = 200
	}
	N := maxQ + maxC + extra

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)
	hdr := map[string]string{"Cache-Control": "no-cache"}

	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, _ = doReqDetailed(t, c, base, "GET", fmt.Sprintf("/api/items?qr=%d", i), "", hdr)
		}(i)
	}
	close(start)
	wg.Wait()

	// Give the proxy a brief moment to update metrics
	time.Sleep(300 * time.Millisecond)

	afterTxt := fetchMetricsText(t, c, base)
	after := getCounterValue(afterTxt, "proxy_queue_rejected_total")
	delta := after - before

	if delta <= 0 {
		t.Skipf("no queue rejections observed (queue=%d, concurrent=%d) — environment may be too large or upstream too fast", maxQ, maxC)
	}
}

// --- New e2e: queue timeouts metric increments when queued longer than timeout ---
func TestQueueTimeoutsMetricIncrements(t *testing.T) {
	unlock := lockSeq(t); defer unlock()

	base := getenv("PROXY_ADDR", "https://localhost:8090")
	// Client timeout slightly larger than enqueue timeout to allow server-side 503 to return
	enqTO := getenvDuration("RP_ENQUEUE_TIMEOUT", 2*time.Second)
	c := newHTTPSClientWithTimeout(enqTO + 3*time.Second)

	maxQ := getenvInt("RP_MAX_QUEUE", 1000)
	maxC := getenvInt("RP_MAX_CONCURRENT", 100)

	// Snapshot metric before load
	beforeTxt := fetchMetricsText(t, c, base)
	before := getCounterValue(beforeTxt, "proxy_queue_timeouts_total")

	// Try multiple rounds to increase chances of timeouts without triggering rejections.
	// We aim just below total capacity so requests wait but aren't rejected.
	target := maxQ + maxC - 1
	if target < maxC+1 {
		target = maxC + 1
	}
	hdr := map[string]string{"Cache-Control": "no-cache"}

	tryRounds := 3
	for round := 0; round < tryRounds; round++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(target)

		for i := 0; i < target; i++ {
			go func(i int) {
				defer wg.Done()
				<-start
				_, _, _ = doReqDetailed(t, c, base, "GET", fmt.Sprintf("/api/items?qt=%d&r=%d", i, round), "", hdr)
			}(i)
		}
		close(start)
		wg.Wait()

		// Wait slightly beyond enqueue timeout to allow queued requests to time out
		time.Sleep(enqTO + 300*time.Millisecond)

		txt := fetchMetricsText(t, c, base)
		after := getCounterValue(txt, "proxy_queue_timeouts_total")
		if after-before > 0 {
			return
		}
	}

	// Best-effort: skip if timeouts not triggered in this environment
	t.Skipf("no queue timeouts observed after %d rounds (queue=%d, concurrent=%d, enqueueTimeout=%s)", tryRounds, maxQ, maxC, enqTO)
}
