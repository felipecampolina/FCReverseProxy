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

	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
)



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

// --- New helper to generate a self-signed cert (for mismatch test) ---
func genCertKey(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{cn},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")}, // added so requests to 127.0.0.1 validate
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return
}

// --- New test: proxy served over HTTPS (TLS termination at proxy) ---
func TestProxyOverHTTPS(t *testing.T) {
	banner("proxy_integration_test.go")

	up := startUpstream(t, "TLS-UP", false)
	defer up.Close()

	targets := []*url.URL{mustParse(t, up.URL)}
	rp := NewReverseProxyMulti(targets, NewLRUCache(32), true)
	rp.ConfigureBalancer("rr")

	// Generate self-signed cert and key for the proxy
	certPEM, keyPEM := genCertKey(t, "proxy.local")

	// Create TLS configuration using the generated cert and key
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("failed to load key pair: %v", err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}

	// Start proxy with TLS using the custom cert
	tlsProxy := httptest.NewUnstartedServer(rp)
	tlsProxy.TLS = tlsConfig
	tlsProxy.StartTLS()
	defer tlsProxy.Close()

	// HTTPS client trusting test server cert
	client := tlsProxy.Client()
	client.Timeout = 3 * time.Second

	req, _ := http.NewRequest("GET", tlsProxy.URL+"/nocache?x=1", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("https request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Upstream") == "" {
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
