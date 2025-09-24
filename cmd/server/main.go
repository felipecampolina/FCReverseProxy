package main

import (
	"log"
	"net/http"
	"traefik-challenge-2/internal/config"
	"traefik-challenge-2/internal/proxy"

	"github.com/joho/godotenv"
)

func main() {
	// Load environment variables from the .env file
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: Could not load .env file (%v), using system environment variables", err)
	}

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
	rp.SetAllowedMethods(cfg.AllowedMethods)

	// Queue configuration comes from config
	qcfg := cfg.Queue

	// Attach queue to proxy (only used for cache misses)
	rp = rp.WithQueue(qcfg)

	// Set up the HTTP server
	mux := http.NewServeMux()
	// Register the proxy directly; queue is applied internally only on cache misses
	mux.Handle("/", rp)

	// Health endpoint (no upstream involved, so do not set X-Upstream)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	log.Printf("Listening on %s, upstreams=%d primary=%s lb=%s cache=%v queue(max=%d,concurrent=%d)",
		cfg.ListenAddr,
		len(cfg.TargetURLs),
		cfg.TargetURL.String(),
		cfg.LoadBalancerStrategy,
		cfg.Cache.Enabled,
		qcfg.MaxQueue,
		qcfg.MaxConcurrent,
	)

	if err := http.ListenAndServe(cfg.ListenAddr, withServerHeaders(mux)); err != nil {
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
