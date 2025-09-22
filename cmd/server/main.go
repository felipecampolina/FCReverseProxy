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

	// Initialize the reverse proxy with cache
	rp := proxy.NewReverseProxy(cfg.TargetURL, proxy.NewLRUCache(cfg.Cache.MaxEntries), cfg.Cache.Enabled)

	// Set up the HTTP server
	mux := http.NewServeMux()
	mux.Handle("/", rp)

	log.Printf("Listening on %s, proxying to %s, cache enabled: %v", cfg.ListenAddr, cfg.TargetURL.String(), cfg.Cache.Enabled)
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