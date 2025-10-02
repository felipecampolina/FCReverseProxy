package main

import (
	"log"
	"net/http"
	"traefik-challenge-2/internal/config"
	"traefik-challenge-2/internal/proxy"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	// Load application configuration from yalm file.
	appConfig, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	// Build the reverse proxy:
	// - Single upstream: reverse proxy
	// - Multiple upstreams: reverse load-balanced proxy
	// - Optional in-memory cache (LRU) controlled by config
	var reverseProxy *proxy.ReverseProxy
	if len(appConfig.TargetURLs) > 1 {
		reverseProxy = proxy.NewReverseProxyMulti(
			appConfig.TargetURLs,
			proxy.NewLRUCache(appConfig.Cache.MaxEntries),
			appConfig.Cache.Enabled,
		)
	} else {
		reverseProxy = proxy.NewReverseProxy(
			appConfig.TargetURL,
			proxy.NewLRUCache(appConfig.Cache.MaxEntries),
			appConfig.Cache.Enabled,
		)
	}

	// Configure load-balancer strategy and health checks.
	reverseProxy.ConfigureBalancer(appConfig.LoadBalancerStrategy)
	reverseProxy.SetHealthCheckEnabled(appConfig.LoadBalancerHealthCheck)

	// Restrict allowed HTTP methods as configured.
	reverseProxy.SetAllowedMethods(appConfig.AllowedMethods)

	// Queue configuration (used only for cache misses inside the proxy).
	queueConfig := appConfig.Queue
	reverseProxy = reverseProxy.WithQueue(queueConfig)

	// Replace inline endpoint registration with helper.
	serverMux := newServerMux(reverseProxy)

	// Startup summary for observability.
	log.Printf(
		"Listening on %s, upstreams=%d primary=%s lb=%s hc=%v cache=%v queue(max=%d,concurrent=%d) tls(enabled=%v)",
		appConfig.ListenAddr,
		len(appConfig.TargetURLs),
		appConfig.TargetURL.String(),
		appConfig.LoadBalancerStrategy,
		appConfig.LoadBalancerHealthCheck,
		appConfig.Cache.Enabled,
		queueConfig.MaxQueue,
		queueConfig.MaxConcurrent,
		appConfig.TLS.Enabled,
	)

	// Start server with consistent server headers.
	if err := startServer(appConfig, withProxyHeaders(serverMux)); err != nil {
		log.Fatal(err)
	}
}
// newServerMux assembles all HTTP endpoints.
func newServerMux(reverseProxy *proxy.ReverseProxy) *http.ServeMux {
	mux := http.NewServeMux()
	// Expose Prometheus metrics.
	mux.Handle("/metrics", promhttp.Handler())
	// Proxy all other requests;
	mux.Handle("/", reverseProxy)
	// Local health endpoint for the proxy.
	mux.HandleFunc("/healthz", healthHandler)
	return mux
}

// healthHandler responds to local health checks.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// withServerHeaders adds a simple Server header to every response.
func withProxyHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "FCReverseProxy/3.0")
		next.ServeHTTP(w, r)
	})
}