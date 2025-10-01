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

// startServer starts an HTTP server if TLS is disabled, otherwise HTTPS.
// If TLS is enabled and no cert/key are provided, a self-signed pair for localhost is generated.
// The handler is the fully-wrapped root HTTP handler.
func startServer(appConfig *config.Config, rootHandler http.Handler) error {
	if !appConfig.TLS.Enabled {
		// Plain HTTP mode
		log.Printf("Starting HTTP on %s", appConfig.ListenAddr)
		return http.ListenAndServe(appConfig.ListenAddr, rootHandler)
	}

	// Provide default filenames if not specified in config.
	if appConfig.TLS.CertFile == "" {
		appConfig.TLS.CertFile = "server.crt"
	}
	if appConfig.TLS.KeyFile == "" {
		appConfig.TLS.KeyFile = "server.key"
	}

	// Ensure there is a certificate pair available (create self-signed if missing).
	if err := ensureSelfSignedIfMissing(appConfig.TLS.CertFile, appConfig.TLS.KeyFile); err != nil {
		log.Printf("TLS enabled but could not create self-signed cert: %v (falling back to HTTP)", err)
		return http.ListenAndServe(appConfig.ListenAddr, rootHandler)
	}

	// If cert/key exist, start HTTPS with a conservative TLS configuration.
	if fileExists(appConfig.TLS.CertFile) && fileExists(appConfig.TLS.KeyFile) {
		server := &http.Server{
			Addr:         appConfig.ListenAddr,
			Handler:      rootHandler,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
			TLSConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		}
		log.Printf("Starting HTTPS (static/self-signed) on %s cert=%s key=%s", appConfig.ListenAddr, appConfig.TLS.CertFile, appConfig.TLS.KeyFile)
		return server.ListenAndServeTLS(appConfig.TLS.CertFile, appConfig.TLS.KeyFile)
	}

	// Safeguard: should not happen since ensureSelfSignedIfMissing already attempted generation.
	log.Printf("TLS enabled but cert/key not present; falling back to HTTP on %s", appConfig.ListenAddr)
	return http.ListenAndServe(appConfig.ListenAddr, rootHandler)
}

// ensureSelfSignedIfMissing generates a localhost self-signed certificate if either file is missing.
func ensureSelfSignedIfMissing(certPath, keyPath string) error {
	if fileExists(certPath) && fileExists(keyPath) {
		return nil
	}
	// Always generate for localhost usage.
	return generateSelfSigned(certPath, keyPath)
}

// fileExists returns true if the path exists (no error from os.Stat).
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// generateSelfSigned creates a 2048-bit RSA key and a self-signed X.509 certificate for "localhost".
func generateSelfSigned(certPath, keyPath string) error {
	// Ensure parent directories exist (skip when target is current directory).
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil && filepath.Dir(certPath) != "." {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil && filepath.Dir(keyPath) != "." {
		return err
	}

	// Generate RSA private key.
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	// Create a random serial number for the certificate.
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return err
	}

	// Define a certificate template valid for 1 year for the DNS name "localhost".
	certTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "localhost",
			Organization: []string{"auto-generated"},
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),      // small backdate to avoid clock skew issues
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // 1 year validity
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	// Self-sign the certificate (template is both cert and parent).
	certDERBytes, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &privateKey.PublicKey, privateKey)
	if err != nil {
		return err
	}

	// Write certificate to disk (PEM-encoded).
	certOutFile, err := os.Create(certPath)
	if err != nil {
		return err
	}
	defer certOutFile.Close()
	if err := pem.Encode(certOutFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDERBytes}); err != nil {
		return err
	}

	// Write private key to disk (PEM-encoded, restricted permissions).
	keyOutFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer keyOutFile.Close()
	if err := pem.Encode(keyOutFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}); err != nil {
		return err
	}

	log.Printf("Generated self-signed certificate (%s, %s) for localhost", certPath, keyPath)
	return nil
}