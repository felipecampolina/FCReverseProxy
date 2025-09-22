package main

import (
	"log"
	"os"

	"traefik-challenge-2/internal/upstream"

	"github.com/joho/godotenv"
)

func main() {
	// Load .env so UPSTREAM_LISTEN can be configured there
	_ = godotenv.Load()

	addr := os.Getenv("UPSTREAM_LISTEN")
	if addr == "" {
		addr = ":8000"
	}

	if err := upstream.Start(addr); err != nil {
		log.Fatal(err)
	}
}
