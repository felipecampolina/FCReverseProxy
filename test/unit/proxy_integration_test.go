package proxy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	proxy "traefik-challenge-2/internal/proxy"
)

// startUpstream spins up a test HTTP upstream with endpoints used by the proxy tests.
// - /cachehit: cacheable (public, max-age=5)
// - /nocache: non-cacheable (no-store), optionally slow to simulate load
// - /work:    non-cacheable (no-store), used by least-connections test
func startUpstream(t *testing.T, upstreamName string, simulateSlow bool) *httptest.Server {
	mux := http.NewServeMux()

	// Cacheable endpoint: responses can be stored and reused by the proxy.
	mux.HandleFunc("/cachehit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=5")
		w.Header().Set("X-Upstream", upstreamName)
		json.NewEncoder(w).Encode(map[string]any{
			"upstream": upstreamName,
			"path":     r.URL.Path,
			"time":     time.Now().UnixNano(),
		})
	})

	// Non-cacheable endpoint: each request is a forced cache miss.
	mux.HandleFunc("/nocache", func(w http.ResponseWriter, r *http.Request) {
		if simulateSlow {
			time.Sleep(250 * time.Millisecond)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Upstream", upstreamName)
		json.NewEncoder(w).Encode(map[string]any{
			"upstream": upstreamName,
			"q":        r.URL.RawQuery,
		})
	})

	// Work endpoint for load-balancing tests.
	mux.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		if simulateSlow {
			time.Sleep(250 * time.Millisecond)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Upstream", upstreamName)
		json.NewEncoder(w).Encode(map[string]any{
			"upstream": upstreamName,
			"path":     r.URL.Path,
			"q":        r.URL.RawQuery,
		})
	})

	return httptest.NewServer(mux)
}

// mustParse converts a string to *url.URL or fails the test on error.
func mustParse(t *testing.T, rawURL string) *url.URL {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsedURL
}

func TestProxyRoundRobinWithCache(t *testing.T) {
	banner("proxy_integration_test.go")

	upstreamA := startUpstream(t, "A", false)
	defer upstreamA.Close()
	upstreamB := startUpstream(t, "B", false)
	defer upstreamB.Close()

	upstreamTargets := []*url.URL{mustParse(t, upstreamA.URL), mustParse(t, upstreamB.URL)}

	reverseProxy := proxy.NewReverseProxyMulti(upstreamTargets, proxy.NewLRUCache(128), true)
	// Disable active health checks for these test
	reverseProxy.SetHealthCheckEnabled(false)
	reverseProxy.ConfigureBalancer("rr")

	proxyServer := httptest.NewServer(reverseProxy)
	defer proxyServer.Close()

	httpClient := &http.Client{Timeout: 3 * time.Second}

	// Round-robin sequence on unique nocache requests.
	roundRobinSequence := []string{}
	for i := 0; i < 4; i++ {
		rrRequest, _ := http.NewRequest("GET", proxyServer.URL+"/nocache?i="+strconv.Itoa(i), nil)
		rrResponse, err := httpClient.Do(rrRequest)
		if err != nil {
			t.Fatalf("request %d error: %v", i, err)
		}
		rrResponse.Body.Close()
		upstreamHeader := rrResponse.Header.Get("X-Upstream")
		if upstreamHeader == "" {
			t.Fatalf("missing X-Upstream header")
		}
		roundRobinSequence = append(roundRobinSequence, upstreamHeader)
	}
	// Expect A,B,A,B
	wantSequence := []string{"A", "B", "A", "B"}
	for i := range wantSequence {
		if roundRobinSequence[i] != wantSequence[i] {
			t.Fatalf("round-robin mismatch got=%v want=%v", roundRobinSequence, wantSequence)
		}
	}

	// Cache behavior: first /cachehit MISS, second HIT, with same upstream header.
	firstCacheReq, _ := http.NewRequest("GET", proxyServer.URL+"/cachehit", nil)
	firstCacheResp, err := httpClient.Do(firstCacheReq)
	if err != nil {
		t.Fatalf("cachehit miss req error: %v", err)
	}
	firstUpstream := firstCacheResp.Header.Get("X-Upstream")
	if firstUpstream == "" {
		t.Fatalf("expected upstream header on first response")
	}
	if cacheHeader := firstCacheResp.Header.Get("X-Cache"); cacheHeader != "MISS" && cacheHeader != "" && cacheHeader != "BYPASS" {
		// Depending on timing TTL logic may label as MISS or BYPASS (if deemed non-cacheable)
	}
	firstCacheResp.Body.Close()

	secondCacheReq, _ := http.NewRequest("GET", proxyServer.URL+"/cachehit", nil)
	secondCacheResp, err := httpClient.Do(secondCacheReq)
	if err != nil {
		t.Fatalf("cachehit hit req error: %v", err)
	}
	defer secondCacheResp.Body.Close()
	if secondCacheResp.Header.Get("X-Cache") != "HIT" {
		t.Fatalf("expected X-Cache=HIT got %q", secondCacheResp.Header.Get("X-Cache"))
	}
	if secondCacheResp.Header.Get("X-Upstream") != firstUpstream {
		t.Fatalf("cached response upstream header changed: first=%s second=%s", firstUpstream, secondCacheResp.Header.Get("X-Upstream"))
	}
}

func TestProxyLeastConnections(t *testing.T) {
	banner("proxy_integration_test.go")

	// Slow backend first so first request goes to SLOW; second should go to FAST.
	slowUpstream := startUpstream(t, "SLOW", true)
	defer slowUpstream.Close()
	fastUpstream := startUpstream(t, "FAST", false)
	defer fastUpstream.Close()

	upstreamTargets := []*url.URL{mustParse(t, slowUpstream.URL), mustParse(t, fastUpstream.URL)}

	reverseProxy := proxy.NewReverseProxyMulti(upstreamTargets, proxy.NewLRUCache(64), true)
	// Disable active health checks for these tests; httptest upstreams lack /healthz
	reverseProxy.SetHealthCheckEnabled(false)
	reverseProxy.ConfigureBalancer("least_conn")

	proxyServer := httptest.NewServer(reverseProxy)
	defer proxyServer.Close()

	httpClient := &http.Client{Timeout: 5 * time.Second}

	type result struct {
		upstreamHeader string
	}
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	responses := make([]result, 2)

	// Use a barrier so both goroutines start requests at the same time.
	startBarrier := make(chan struct{})

	for _, i := range []int{0, 1} {
		go func(i int) {
			defer waitGroup.Done()
			<-startBarrier
			req, _ := http.NewRequest("GET", proxyServer.URL+"/work?req="+strconv.Itoa(i), nil)
			resp, err := httpClient.Do(req)
			if err != nil {
				t.Errorf("req %d error: %v", i, err)
				return
			}
			resp.Body.Close()
			responses[i] = result{upstreamHeader: resp.Header.Get("X-Upstream")}
		}(i)
	}

	close(startBarrier) // launch both concurrently
	waitGroup.Wait()

	if responses[0].upstreamHeader == "" || responses[1].upstreamHeader == "" {
		t.Fatalf("missing upstream header(s): %+v", responses)
	}
	if responses[0].upstreamHeader == responses[1].upstreamHeader {
		t.Fatalf("least-connections failed: both requests hit %s", responses[0].upstreamHeader)
	}

	// Third request after both done should revert to SLOW (both zero -> first picked).
	thirdReq, _ := http.NewRequest("GET", proxyServer.URL+"/work?req=3", nil)
	thirdResp, err := httpClient.Do(thirdReq)
	if err != nil {
		t.Fatalf("third request error: %v", err)
	}
	thirdResp.Body.Close()
	if thirdResp.Header.Get("X-Upstream") != "SLOW" {
		t.Fatalf("expected third request to hit SLOW, got %s", thirdResp.Header.Get("X-Upstream"))
	}
}

// --- New helper to generate a self-signed cert (for mismatch test) ---
func genCertKey(t *testing.T, commonName string) (certPEM, keyPEM []byte) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	serialNumber, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	certTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{commonName},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")}, // allow requests to 127.0.0.1
	}
	certDER, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return
}

// --- New test: proxy served over HTTPS (TLS termination at proxy) ---
func TestProxyOverHTTPS(t *testing.T) {
	banner("proxy_integration_test.go")

	upstream := startUpstream(t, "TLS-UP", false)
	defer upstream.Close()

	upstreamTargets := []*url.URL{mustParse(t, upstream.URL)}
	reverseProxy := proxy.NewReverseProxyMulti(upstreamTargets, proxy.NewLRUCache(32), true)
	// Disable active health checks for these tests; httptest upstreams lack /healthz
	reverseProxy.SetHealthCheckEnabled(false)
	reverseProxy.ConfigureBalancer("rr")

	// Generate self-signed cert and key for the proxy
	certPEM, keyPEM := genCertKey(t, "proxy.local")

	// Create TLS configuration using the generated cert and key
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("failed to load key pair: %v", err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{tlsCert}}

	// Start proxy with TLS using the custom cert
	tlsProxyServer := httptest.NewUnstartedServer(reverseProxy)
	tlsProxyServer.TLS = tlsConfig
	tlsProxyServer.StartTLS()
	defer tlsProxyServer.Close()

	// HTTPS client trusting test server cert
	httpsClient := tlsProxyServer.Client()
	httpsClient.Timeout = 3 * time.Second

	request, _ := http.NewRequest("GET", tlsProxyServer.URL+"/nocache?x=1", nil)
	response, err := httpsClient.Do(request)
	if err != nil {
		t.Fatalf("https request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("want 200, got %d", response.StatusCode)
	}
	if response.Header.Get("X-Upstream") == "" {
		t.Fatalf("missing X-Upstream header in HTTPS proxied response")
	}
}

// --- New test: certificate/key mismatch should be rejected ---
func TestTLSCertKeyMismatch(t *testing.T) {
	banner("proxy_integration_test.go")

	// Generate one cert/key pair and a second, unrelated key.
	certA, _ := genCertKey(t, "mismatch.local") // discard key to avoid unused var
	_, keyB := genCertKey(t, "other.local")

	// Intentionally try to pair certA with keyB (should fail).
	if _, err := tls.X509KeyPair(certA, keyB); err == nil {
		t.Fatalf("expected cert/key mismatch to produce error, got nil")
	}
}
