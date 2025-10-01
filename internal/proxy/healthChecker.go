package proxy

import (
	"net/http"
	"net/url"
	"time"
)

// healthProbeHTTPClient is a shared HTTP client for health probes with a short timeout.

var healthProbeHTTPClient = &http.Client{
	Timeout: 500 * time.Millisecond,
}

func isTargetHealthy(targetURL *url.URL) bool {
	// Build absolute health URL at root (/healthz).
	scheme := targetURL.Scheme
	if scheme == "" {
		scheme = "http"
	}
	healthURL := &url.URL{
		Scheme: scheme,
		Host:   targetURL.Host,
		Path:   "/healthz",
	}
	healthRequest, err := http.NewRequest("GET", healthURL.String(), nil)
	if err != nil {
		return false
	}
	// Hint to avoid connection reuse issues on failing endpoints.
	healthRequest.Close = true

	healthResponse, err := healthProbeHTTPClient.Do(healthRequest)
	if err != nil {
		return false
	}
	defer healthResponse.Body.Close()
	// Consider 2xx/3xx as healthy.
	return healthResponse.StatusCode >= 200 && healthResponse.StatusCode < 400
}
