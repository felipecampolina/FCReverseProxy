package proxy_test

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

	config "traefik-challenge-2/internal/config"
)

// --- Helpers ---

// withEnvs sets env vars for the duration of fn and restores originals after.
// Useful for testing configuration that depends on environment variables.
func withEnvs(t *testing.T, envVars map[string]string, testFn func()) {
	t.Helper()
	originalValues := map[string]*string{}

	for key, value := range envVars {
		if oldVal, ok := os.LookupEnv(key); ok {
			tmpVal := oldVal
			originalValues[key] = &tmpVal
		} else {
			originalValues[key] = nil
		}
		if err := os.Setenv(key, value); err != nil {
			t.Fatalf("set env %s: %v", key, err)
		}
	}

	testFn()

	for key, oldVal := range originalValues {
		if oldVal == nil {
			_ = os.Unsetenv(key)
		} else {
			_ = os.Setenv(key, *oldVal)
		}
	}
}

// genSelfSignedCert generates a self-signed ECDSA certificate for hostname,
// valid for validityPeriod. Returns PEM-encoded cert and key.
func genSelfSignedCert(t *testing.T, hostname string, validityPeriod time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	certTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkixName(hostname),
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     time.Now().Add(validityPeriod),
		DNSNames:     []string{hostname},
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return
}

// pkixName builds a simple PKIX subject using the provided common name.
func pkixName(commonName string) pkix.Name {
	return pkix.Name{CommonName: commonName}
}

// --- Tests ---

func TestTLSConfig_StaticCert_EnvParsing(t *testing.T) {
	// Generate a self-signed certificate for testing.
	certPEM, keyPEM := genSelfSignedCert(t, "local.test", time.Hour)

	// Write the cert and key to temporary files.
	tempDir := t.TempDir()
	certFilePath := filepath.Join(tempDir, "server.crt")
	keyFilePath := filepath.Join(tempDir, "server.key")
	if err := os.WriteFile(certFilePath, certPEM, 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFilePath, keyPEM, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	// Prepare a temporary YAML config file at the default expected location: ./configs/config.yaml.
	configsDir := filepath.Join(tempDir, "configs")
	if err := os.MkdirAll(configsDir, 0700); err != nil {
		t.Fatalf("mkdir configs: %v", err)
	}
	configPath := filepath.Join(configsDir, "config.yaml")
	configYAML := []byte(`
proxy:
  listen: ":0"
  targets: ["http://localhost:9000"]
  tls:
    enabled: true
    cert_file: '` + filepath.ToSlash(certFilePath) + `'
    key_file: '` + filepath.ToSlash(keyFilePath) + `'
`)
	if err := os.WriteFile(configPath, configYAML, 0600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}

	// Change working directory so the default relative path is resolved.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	// Load the config and validate TLS fields were parsed.
	cfgLoaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfgLoaded.TLS.Enabled {
		t.Fatalf("expected TLS enabled")
	}

	// Normalize path separators for cross-platform comparison.
	gotCert := filepath.Clean(filepath.FromSlash(cfgLoaded.TLS.CertFile))
	gotKey := filepath.Clean(filepath.FromSlash(cfgLoaded.TLS.KeyFile))
	wantCert := filepath.Clean(certFilePath)
	wantKey := filepath.Clean(keyFilePath)
	if gotCert != wantCert || gotKey != wantKey {
		t.Fatalf("cert/key mismatch: got cert=%s key=%s, want cert=%s key=%s", gotCert, gotKey, wantCert, wantKey)
	}
}

func TestTLS_StaticHandshake(t *testing.T) {
	// Generate a self-signed certificate used by the test HTTPS server.
	certPEM, keyPEM := genSelfSignedCert(t, "local.test", time.Hour)

	// Write cert and key to temp files for ServeTLS.
	tempDir := t.TempDir()
	certFilePath := filepath.Join(tempDir, "cert.pem")
	keyFilePath := filepath.Join(tempDir, "key.pem")
	if err := os.WriteFile(certFilePath, certPEM, 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFilePath, keyPEM, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	// Start an HTTPS server with the generated cert.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	go func() {
		_ = server.ServeTLS(listener, certFilePath, keyFilePath)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	// HTTP client that skips verification for this test, but asserts SNI.
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // test-only: do not use in production
				ServerName:         "local.test",
			},
		},
		Timeout: 3 * time.Second,
	}

	// Perform a request and validate handshake succeeded and response is OK.
	resp, err := httpClient.Get("https://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("unexpected body %q", body)
	}

	// Ensure TLS connection state and peer certificate are present.
	if resp.TLS == nil {
		t.Fatalf("no TLS connection state")
	}
	if len(resp.TLS.PeerCertificates) == 0 {
		t.Fatalf("no peer certs")
	}
}