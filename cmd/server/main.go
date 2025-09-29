package main

import (
	"log"
	"net/http"
	"traefik-challenge-2/internal/config"
	"traefik-challenge-2/internal/proxy"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	var rp *proxy.ReverseProxy
	if len(cfg.TargetURLs) > 1 {
		rp = proxy.NewReverseProxyMulti(cfg.TargetURLs, proxy.NewLRUCache(cfg.Cache.MaxEntries), cfg.Cache.Enabled)
	} else {
		rp = proxy.NewReverseProxy(cfg.TargetURL, proxy.NewLRUCache(cfg.Cache.MaxEntries), cfg.Cache.Enabled)
	}
	rp.ConfigureBalancer(cfg.LoadBalancerStrategy)
	// New: enable/disable active health checks in the balancer from config
	rp.SetHealthCheckEnabled(cfg.LoadBalancerHealthCheck)
	rp.SetAllowedMethods(cfg.AllowedMethods)

	// Queue configuration comes from config
	qcfg := cfg.Queue

	// Attach queue to proxy (only used for cache misses)
	rp = rp.WithQueue(qcfg)

	// Set up the HTTP server
	mux := http.NewServeMux()

	// Metrics endpoint (must not be proxied)
	mux.Handle("/metrics", promhttp.Handler())

	// Register the proxy directly; queue is applied internally only on cache misses
	mux.Handle("/", rp)

	// Health endpoint (no upstream involved, so do not set X-Upstream)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	log.Printf("Listening on %s, upstreams=%d primary=%s lb=%s hc=%v cache=%v queue(max=%d,concurrent=%d) tls(enabled=%v)",
		cfg.ListenAddr,
		len(cfg.TargetURLs),
		cfg.TargetURL.String(),
		cfg.LoadBalancerStrategy,
		cfg.LoadBalancerHealthCheck,
		cfg.Cache.Enabled,
		qcfg.MaxQueue,
		qcfg.MaxConcurrent,
		cfg.TLS.Enabled,
	)

	// Replaced old inline TLS / HTTP start logic:
	if err := startServer(cfg, withServerHeaders(mux)); err != nil {
		log.Fatal(err)
	}
}

// Adds extra server headers to the response
func withServerHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "go-rp/0.1")
		next.ServeHTTP(w, r)
	})
}