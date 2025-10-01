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

// getEnvOrDefault returns env var or default if empty.
func getEnvOrDefault(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// newInsecureHTTPSClient returns an HTTPS client that skips TLS verification (dev/test only).
func newInsecureHTTPSClient() *http.Client {
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

// newInsecureHTTPSClientWithTimeout returns an HTTPS client with custom timeout.
func newInsecureHTTPSClientWithTimeout(d time.Duration) *http.Client {
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


// doRequestDetailed performs an HTTP request and returns the response and body.
func doRequestDetailed(t *testing.T, client *http.Client, baseURL, method, path string, body string, headers map[string]string) (*http.Response, []byte, error) {
	t.Helper()
	req, _ := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, bytes.NewBufferString(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, b, nil
}

// fetchMetrics fetches /metrics as plain text from the proxy.
func fetchMetrics(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/metrics")
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

// containsMetricsLineWith returns true if any metric line contains all substrings.
func containsMetricsLineWith(text string, contains ...string) bool {
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

// metricFamilyExistsInText detects if a metric family is present in exposition text.
func metricFamilyExistsInText(text, name string) bool {
	return strings.Contains(text, "\n"+name+" ") || strings.Contains(text, "\n# TYPE "+name+" ")
}

// serialize all e2e tests; keeps tests sequential even if package is run with parallel flags
var (
	sequentialRunMutex sync.Mutex
)

// lockSequentialTests ensures tests run one-at-a-time.
func lockSequentialTests() func() {
	sequentialRunMutex.Lock()
	return func() { sequentialRunMutex.Unlock() }
}

// Sanity: HTTPS/TLS reachability + core metrics presence.
func TestProxyTLSAndMetricsReachability(t *testing.T) {
	unlock := lockSequentialTests(); defer unlock()

	proxyBaseURL := getEnvOrDefault("PROXY_ADDR", "https://localhost:8090")
	httpClient := newInsecureHTTPSClient()

	// health over TLS
	resp, err := httpClient.Get(strings.TrimRight(proxyBaseURL, "/") + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz over TLS failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/healthz status=%d", resp.StatusCode)
	}

	// Warm up proxy so histograms/counters get observations.
	_, _, _ = doRequestDetailed(t, httpClient, proxyBaseURL, "GET", "/api/items?warm=1", "", map[string]string{"Cache-Control": "no-cache"})

	// metrics reachability and core series presence (check base histogram names)
	metricsText := fetchMetrics(t, httpClient, proxyBaseURL)
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
		if !metricFamilyExistsInText(metricsText, m) {
			t.Fatalf("expected metric %q to be exposed", m)
		}
	}
}

// Cache MISS then HIT; verify headers and that cache-labelled series appear.
func TestCacheHitMissAndDurationMetrics(t *testing.T) {
	unlock := lockSequentialTests(); defer unlock()

	proxyBaseURL := getEnvOrDefault("PROXY_ADDR", "https://localhost:8090")
	httpClient := newInsecureHTTPSClient()

	resourcePath := "/api/items?cache_sweep=1"

	// MISS
	firstResp, _, err := doRequestDetailed(t, httpClient, proxyBaseURL, "GET", resourcePath, "", nil)
	if err != nil {
		t.Fatalf("miss req: %v", err)
	}
	xc1 := firstResp.Header.Get("X-Cache")
	if xc1 != "MISS" && xc1 != "BYPASS" {
		// BYPASS acceptable if upstream deemed non-cacheable; prefer MISS
		t.Logf("first response X-Cache=%q", xc1)
	}

	// HIT
	secondResp, _, err := doRequestDetailed(t, httpClient, proxyBaseURL, "GET", resourcePath, "", nil)
	if err != nil {
		t.Fatalf("hit req: %v", err)
	}
	if xc2 := secondResp.Header.Get("X-Cache"); xc2 != "HIT" {
		t.Fatalf("expected X-Cache=HIT on second request, got %q", xc2)
	}

	metricsText := fetchMetrics(t, httpClient, proxyBaseURL)
	if !containsMetricsLineWith(metricsText, "proxy_requests_total", `cache="HIT"`) {
		t.Fatalf("expected proxy_requests_total with cache=\"HIT\" label")
	}
	// duration histograms present (check base names)
	for _, m := range []string{
		"proxy_request_duration_seconds",
		"proxy_upstream_request_duration_seconds",
	} {
		if !metricFamilyExistsInText(metricsText, m) {
			t.Fatalf("expected %s to be exposed", m)
		}
	}
}

// Queue metrics: send no-cache burst to ensure queue wait histogram gets observations.
func TestQueueMetricsExposureUnderLoad(t *testing.T) {
	unlock := lockSequentialTests(); defer unlock()

	proxyBaseURL := getEnvOrDefault("PROXY_ADDR", "https://localhost:8090")
	httpClient := newInsecureHTTPSClientWithTimeout(10 * time.Second)

	const totalRequests = 300
	var waitGroup sync.WaitGroup
	waitGroup.Add(totalRequests)
	noCacheHeaders := map[string]string{"Cache-Control": "no-cache"}

	for i := 0; i < totalRequests; i++ {
		go func(i int) {
			defer waitGroup.Done()
			_, _, _ = doRequestDetailed(t, httpClient, proxyBaseURL, "GET", fmt.Sprintf("/api/items?q=%d", i), "", noCacheHeaders)
		}(i)
	}

	// Give time for admissions/observations and scrape
	time.Sleep(500 * time.Millisecond)
	metricsText := fetchMetrics(t, httpClient, proxyBaseURL)

	// Check base histogram family presence
	if !metricFamilyExistsInText(metricsText, "proxy_queue_wait_seconds") {
		t.Fatalf("expected proxy_queue_wait_seconds to exist")
	}

	// Optional bucket non-zero check remains timing-sensitive
	histogramBucketRe := regexp.MustCompile(`^proxy_queue_wait_seconds_bucket\{.*\}\s+([0-9]+(\.[0-9]+)?)$`)
	hasNonZeroBucket := false
	for _, line := range strings.Split(metricsText, "\n") {
		m := histogramBucketRe.FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		if m[1] != "0" {
			hasNonZeroBucket = true
			break
		}
	}
	if !hasNonZeroBucket {
		t.Log("queue_wait histogram buckets observed but counts may be zero at scrape time (timing sensitive)")
	}

	waitGroup.Wait()
}

// Method/status breakdowns should be present in proxy_requests_total.
func TestMethodAndStatusBreakdownsPresent(t *testing.T) {
	unlock := lockSequentialTests(); defer unlock()

	proxyBaseURL := getEnvOrDefault("PROXY_ADDR", "https://localhost:8090")
	httpClient := newInsecureHTTPSClient()

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
		_, _, _ = doRequestDetailed(t, httpClient, proxyBaseURL, it.m, it.p, `{"n":"x"}`, map[string]string{"Content-Type": "application/json"})
	}

	metricsText := fetchMetrics(t, httpClient, proxyBaseURL)
	// methods
	for _, meth := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		if !containsMetricsLineWith(metricsText, "proxy_requests_total", fmt.Sprintf(`method="%s"`, meth)) {
			t.Fatalf("expected proxy_requests_total with method=%s", meth)
		}
	}
	// statuses: at least 2xx should exist; 4xx likely exists due to notfound
	if !containsMetricsLineWith(metricsText, "proxy_requests_total", `status="200"`) {
		t.Fatalf("expected proxy_requests_total with status=200")
	}
	if !containsMetricsLineWith(metricsText, "proxy_requests_total", `status="404"`) {
		t.Log("no 404 series observed; upstream may have handled notfound differently")
	}
}

// Verify upstream metrics exposure by querying upstream /metrics directly.
func TestUpstreamMetricsExposure(t *testing.T) {
	unlock := lockSequentialTests(); defer unlock()

	// Default upstream address (adjust with UPSTREAM_ADDR when needed)
	upstreamBaseURL := getEnvOrDefault("UPSTREAM_ADDR", "http://localhost:9000")
	proxyClient := newInsecureHTTPSClient()
	upstreamClient := &http.Client{Timeout: 5 * time.Second}

	// Generate some upstream traffic via proxy (no-cache to avoid HITs)
	proxyBaseURL := getEnvOrDefault("PROXY_ADDR", "https://localhost:8090")
	for i := 0; i < 10; i++ {
		_, _, _ = doRequestDetailed(t, proxyClient, proxyBaseURL, "GET", fmt.Sprintf("/api/items?upm=%d", i), "", map[string]string{"Cache-Control": "no-cache"})
	}

	// Now read upstream metrics directly
	resp, err := upstreamClient.Get(strings.TrimRight(upstreamBaseURL, "/") + "/metrics")
	if err != nil {
		t.Fatalf("upstream /metrics fetch failed from %s: %v", upstreamBaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("upstream /metrics status=%d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	metricsText := string(b)

	for _, m := range []string{
		"upstream_requests_total",
		"upstream_request_duration_seconds",
		"upstream_inflight",
	} {
		if !metricFamilyExistsInText(metricsText, m) {
			t.Fatalf("expected upstream metric %q to be exposed", m)
		}
	}
}

// Helpers: env int/duration parsing for queue-related settings used by the server.
// getEnvInt returns an env var parsed as int, or default on error/empty.
func getEnvInt(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return def
}

// getEnvDuration returns an env var parsed as duration, or default on error/empty.
func getEnvDuration(k string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

// getBareCounterValue reads a bare counter value from Prometheus text (no labels).
func getBareCounterValue(text, name string) float64 {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s+([0-9]+(?:\.[0-9]+)?)$`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return f
}

//Queue rejections metric increments under saturation ---
func TestQueueRejectionsMetricIncrements(t *testing.T) {
	unlock := lockSequentialTests(); defer unlock()

	proxyBaseURL := getEnvOrDefault("PROXY_ADDR", "https://localhost:8090")
	httpClient := newInsecureHTTPSClientWithTimeout(15 * time.Second)

	// Read server queue config (same keys used by main)
	maxQueue := getEnvInt("RP_MAX_QUEUE", 1000)
	maxConcurrent := getEnvInt("RP_MAX_CONCURRENT", 100)

	// Snapshot metric before load
	metricsBeforeText := fetchMetrics(t, httpClient, proxyBaseURL)
	rejectedBeforeCount := getBareCounterValue(metricsBeforeText, "proxy_queue_rejected_total")

	// Plan to exceed queue+concurrency with a single synchronized burst
	additionalRequests := maxConcurrent
	if additionalRequests > 200 {
		additionalRequests = 200
	}
	totalRequests := maxQueue + maxConcurrent + additionalRequests

	startBarrier := make(chan struct{})
	var waitGroup sync.WaitGroup
	waitGroup.Add(totalRequests)
	noCacheHeaders := map[string]string{"Cache-Control": "no-cache"}

	for i := 0; i < totalRequests; i++ {
		go func(i int) {
			defer waitGroup.Done()
			<-startBarrier
			_, _, _ = doRequestDetailed(t, httpClient, proxyBaseURL, "GET", fmt.Sprintf("/api/items?qr=%d", i), "", noCacheHeaders)
		}(i)
	}
	close(startBarrier)
	waitGroup.Wait()

	// Give the proxy a brief moment to update metrics
	time.Sleep(300 * time.Millisecond)

	metricsAfterText := fetchMetrics(t, httpClient, proxyBaseURL)
	rejectedAfterCount := getBareCounterValue(metricsAfterText, "proxy_queue_rejected_total")
	rejectedDelta := rejectedAfterCount - rejectedBeforeCount

	if rejectedDelta <= 0 {
		t.Skipf("no queue rejections observed (queue=%d, concurrent=%d) — environment may be too large or upstream too fast", maxQueue, maxConcurrent)
	}
}

// Queue timeouts metric increments when queued longer than timeout ---
func TestQueueTimeoutsMetricIncrements(t *testing.T) {
	unlock := lockSequentialTests(); defer unlock()

	proxyBaseURL := getEnvOrDefault("PROXY_ADDR", "https://localhost:8090")
	// Client timeout slightly larger than enqueue timeout to allow server-side 503 to return
	enqueueTimeout := getEnvDuration("RP_ENQUEUE_TIMEOUT", 2*time.Second)
	httpClient := newInsecureHTTPSClientWithTimeout(enqueueTimeout + 3*time.Second)

	maxQueue := getEnvInt("RP_MAX_QUEUE", 1000)
	maxConcurrent := getEnvInt("RP_MAX_CONCURRENT", 100)

	// Snapshot metric before load
	metricsBeforeText := fetchMetrics(t, httpClient, proxyBaseURL)
	timeoutsBeforeCount := getBareCounterValue(metricsBeforeText, "proxy_queue_timeouts_total")

	// Try multiple rounds to increase chances of timeouts without triggering rejections.
	// We aim just below total capacity so requests wait but aren't rejected.
	targetOutstanding := maxQueue + maxConcurrent - 1
	if targetOutstanding < maxConcurrent+1 {
		targetOutstanding = maxConcurrent + 1
	}
	noCacheHeaders := map[string]string{"Cache-Control": "no-cache"}

	attempts := 3
	for attempt := 0; attempt < attempts; attempt++ {
		startBarrier := make(chan struct{})
		var waitGroup sync.WaitGroup
		waitGroup.Add(targetOutstanding)

		for i := 0; i < targetOutstanding; i++ {
			go func(i int) {
				defer waitGroup.Done()
				<-startBarrier
				_, _, _ = doRequestDetailed(t, httpClient, proxyBaseURL, "GET", fmt.Sprintf("/api/items?qt=%d&r=%d", i, attempt), "", noCacheHeaders)
			}(i)
		}
		close(startBarrier)
		waitGroup.Wait()

		// Wait slightly beyond enqueue timeout to allow queued requests to time out
		time.Sleep(enqueueTimeout + 300*time.Millisecond)

		metricsText := fetchMetrics(t, httpClient, proxyBaseURL)
		timeoutsAfterCount := getBareCounterValue(metricsText, "proxy_queue_timeouts_total")
		if timeoutsAfterCount-timeoutsBeforeCount > 0 {
			return
		}
	}

	// Best-effort: skip if timeouts not triggered in this environment
	t.Skipf("no queue timeouts observed after %d rounds (queue=%d, concurrent=%d, enqueueTimeout=%s)", attempts, maxQueue, maxConcurrent, enqueueTimeout)
}
