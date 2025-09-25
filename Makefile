# Makefile for traefik-challenge-2

# Cross-platform shell and helpers for metrics
ifeq ($(OS),Windows_NT)
  SHELL := cmd.exe
  .SHELLFLAGS := /C
  PROM_FILE := $(CURDIR)\prometheus.yml
  FILE_CHECK := if not exist prometheus.yml ( echo prometheus.yml not found in project root & exit 1 )
  RM_PROM := -@docker rm -f prometheus >NUL 2>&1
  RM_GRAF := -@docker rm -f grafana >NUL 2>&1
else
  SHELL := /bin/sh
  .SHELLFLAGS := -c
  PROM_FILE := $${PWD}/prometheus.yml
  FILE_CHECK := if [ ! -f prometheus.yml ]; then echo "prometheus.yml not found in project root"; exit 1; fi
  RM_PROM := -@docker rm -f prometheus >/dev/null 2>&1 || true
  RM_GRAF := -@docker rm -f grafana >/dev/null 2>&1 || true
endif

.PHONY: build test clean env run deps
deps:
	go get github.com/joho/godotenv
	go get github.com/prometheus/client_golang/prometheus
	go get github.com/prometheus/client_golang/prometheus/promhttp

run: deps
	go run -mod=mod ./cmd/server

build:
	mkdir -p bin
	go build -o bin/server ./cmd/server
	go build -o bin/upstream ./cmd/upstream


test:
	go test ./internal/proxy -v
	go test ./internal/config -v

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

# --- Metrics stack (Prometheus + Grafana) ---
.PHONY: run-metrics stop-metrics
run-metrics:
	$(FILE_CHECK)
	$(RM_PROM)
	$(RM_GRAF)
	docker run -d --name=prometheus -p 9090:9090 -v "$(PROM_FILE)":/etc/prometheus/prometheus.yml prom/prometheus
	docker run -d -p 3000:3000 --name=grafana grafana/grafana-oss

stop-metrics:
	$(RM_PROM)
	$(RM_GRAF)
