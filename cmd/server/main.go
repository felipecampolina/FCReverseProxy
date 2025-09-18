package main

import (
	"log"
	"net/http"

	"github.com/joho/godotenv"

	"traefik-challenge-2/internal/config"
	"traefik-challenge-2/internal/proxy"
)

func main() {
	// carrega variáveis de ambiente do arquivo .env
	if err := godotenv.Load(); err != nil {
		log.Printf("aviso: não foi possível carregar .env (%v), usando env do sistema", err)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	rp := proxy.NewReverseProxy(cfg.TargetURL, proxy.NewLRUCache(cfg.Cache.MaxEntries), cfg.Cache.Enabled)

	mux := http.NewServeMux()
	mux.Handle("/", rp)

	log.Printf("listening on %s, proxying to %s, cache=%v", cfg.ListenAddr, cfg.TargetURL.String(), cfg.Cache.Enabled)
	if err := http.ListenAndServe(cfg.ListenAddr, withServerHeaders(mux)); err != nil {
		log.Fatal(err)
	}
}

// cabeçalhos extras de servidor
func withServerHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "go-rp/0.1")
		next.ServeHTTP(w, r)
	})
}