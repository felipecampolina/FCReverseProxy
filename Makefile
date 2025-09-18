# Makefile for traefik-challenge-2


.PHONY: build test clean env run deps
deps:
	go get github.com/joho/godotenv

run: deps
	go run -mod=mod ./cmd/server

build:
	go build -o bin/traefik-challenge-2 ./cmd/...

test:
	go test ./...

clean:
	rm -rf bin/

env:
	echo "PROXY_LISTEN=:80" > .env
	echo "PROXY_TARGET=http://host.docker.internal:9000" >> .env
