package config_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"traefik-challenge-2/internal/config"
)

// --- Helpers ---

func withEnvs(t *testing.T, kv map[string]string, fn func()) {
	t.Helper()
	orig := map[string]*string{}
	for k, v := range kv {
		if ov, ok := os.LookupEnv(k); ok {
			tmp := ov
			orig[k] = &tmp
		} else {
			orig[k] = nil
		}
		if err := os.Setenv(k, v); err != nil {
			t.Fatalf("set env %s: %v", k, err)
		}
	}
	fn()
	for k, ov := range orig {
		if ov == nil {
			_ = os.Unsetenv(k)
		} else {
			_ = os.Setenv(k, *ov)
		}
	}
}

func genSelfSignedCert(t *testing.T, host string, validFor time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkixName(host),
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     time.Now().Add(validFor),
		DNSNames:     []string{host},
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	b, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b})
	return
}

func pkixName(cn string) pkix.Name {
	return pkix.Name{CommonName: cn}
}

// --- Tests ---

func TestTLSConfig_StaticCert_EnvParsing(t *testing.T) {
	withEnvs(t, map[string]string{
		"PROXY_TARGET":        "http://localhost:9000",
		"PROXY_TLS_ENABLED":   "true",
		"PROXY_TLS_CERT_FILE": "/tmp/server.crt",
		"PROXY_TLS_KEY_FILE":  "/tmp/server.key",
	}, func() {
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.TLS.Enabled {
			t.Fatalf("expected TLS enabled")
		}
		if cfg.TLS.CertFile != "/tmp/server.crt" || cfg.TLS.KeyFile != "/tmp/server.key" {
			t.Fatalf("cert/key mismatch: %+v", cfg.TLS)
		}
	})
}

func TestTLS_StaticHandshake(t *testing.T) {
	certPEM, keyPEM := genSelfSignedCert(t, "local.test", time.Hour)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}

	go func() {
		_ = srv.ServeTLS(ln, certPath, keyPath)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
	})

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // test-only
				ServerName:         "local.test",
			},
		},
		Timeout: 3 * time.Second,
	}

	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "ok" {
		t.Fatalf("unexpected body %q", b)
	}
	if resp.TLS == nil {
		t.Fatalf("no TLS connection state")
	}
	if len(resp.TLS.PeerCertificates) == 0 {
		t.Fatalf("no peer certs")
	}
}