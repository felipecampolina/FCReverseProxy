package main

import (
	"log"
	"os"
	"strings"
	"sync"

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

	// Support multiple comma-separated addresses
	if strings.Contains(addr, ",") {
		parts := strings.Split(addr, ",")
		var wg sync.WaitGroup
		for _, p := range parts {
			a := strings.TrimSpace(p)
			if a == "" {
				continue
			}
			wg.Add(1)
			go func(ad string) {
				defer wg.Done()
				log.Printf("starting upstream server on %s", ad)
				if err := upstream.Start(ad); err != nil {
					log.Printf("upstream server %s exited: %v", ad, err)
				}
			}(a)
		}
		wg.Wait()
		return
	}

	if err := upstream.Start(addr); err != nil {
		log.Fatal(err)
	}
}
