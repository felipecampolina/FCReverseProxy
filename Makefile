# Makefile for traefik-challenge-2


.PHONY: build test clean env run deps
deps:
	go get github.com/joho/godotenv

run: deps
	go run -mod=mod ./cmd/server

build:
	mkdir -p bin
	go build -o bin/server ./cmd/server
	go build -o bin/upstream ./cmd/upstream


test:
	go test ./internal/proxy -v

clean:
	rm -rf bin/

env:
	echo "PROXY_LISTEN=:80" > .env
	echo "PROXY_TARGET=http://host.docker.internal:9000" >> .env

.PHONY: run-proxy run-upstream run-demo
run-proxy: deps
	go run -mod=mod ./cmd/server

run-upstream: deps
	go run -mod=mod ./cmd/upstream

# Run both upstream and proxy concurrently (requires make -j)
run-demo: deps
	$(MAKE) -j2 run-upstream run-proxy
