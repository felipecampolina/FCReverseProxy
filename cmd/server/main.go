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

	// carrega config
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	// cria reverse proxy
	rp := proxy.NewReverseProxy(cfg.TargetURL)

	// rotas
	mux := http.NewServeMux()
	mux.Handle("/", rp)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("reverse proxy escutando em %s -> %s", cfg.ListenAddr, cfg.TargetURL.String())

	// inicia servidor
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
