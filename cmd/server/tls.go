package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"traefik-challenge-2/internal/config"
)

// startServer: HTTP if disabled; otherwise static cert or auto-generated self-signed (localhost).
func startServer(cfg *config.Config, handler http.Handler) error {
	if !cfg.TLS.Enabled {
		log.Printf("Starting HTTP on %s", cfg.ListenAddr)
		return http.ListenAndServe(cfg.ListenAddr, handler)
	}

	// Provide default filenames if not specified.
	if cfg.TLS.CertFile == "" {
		cfg.TLS.CertFile = "server.crt"
	}
	if cfg.TLS.KeyFile == "" {
		cfg.TLS.KeyFile = "server.key"
	}

	// Ensure self-signed exists if either missing.
	if err := ensureSelfSignedIfMissing(cfg.TLS.CertFile, cfg.TLS.KeyFile); err != nil {
		log.Printf("TLS enabled but could not create self-signed cert: %v (falling back to HTTP)", err)
		return http.ListenAndServe(cfg.ListenAddr, handler)
	}

	if fileExists(cfg.TLS.CertFile) && fileExists(cfg.TLS.KeyFile) {
		srv := &http.Server{
			Addr:         cfg.ListenAddr,
			Handler:      handler,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		}
		log.Printf("Starting HTTPS (static/self-signed) on %s cert=%s key=%s", cfg.ListenAddr, cfg.TLS.CertFile, cfg.TLS.KeyFile)
		return srv.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	}

	log.Printf("TLS enabled but cert/key not present; falling back to HTTP on %s", cfg.ListenAddr)
	return http.ListenAndServe(cfg.ListenAddr, handler)
}

func ensureSelfSignedIfMissing(certPath, keyPath string) error {
	if fileExists(certPath) && fileExists(keyPath) {
		return nil
	}
	// Always generate for localhost usage.
	return generateSelfSigned(certPath, keyPath)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func generateSelfSigned(certPath, keyPath string) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil && filepath.Dir(certPath) != "." {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil && filepath.Dir(keyPath) != "." {
		return err
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "localhost",
			Organization: []string{"auto-generated"},
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	cf, err := os.Create(certPath)
	if err != nil {
		return err
	}
	defer cf.Close()
	if err := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return err
	}

	kf, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer kf.Close()
	if err := pem.Encode(kf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}); err != nil {
		return err
	}

	log.Printf("Generated self-signed certificate (%s, %s) for localhost", certPath, keyPath)
	return nil
}