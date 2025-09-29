# Makefile for traefik-challenge-2

# Cross-platform shell and helpers for metrics
ifeq ($(OS),Windows_NT)
  SHELL := cmd.exe
  .SHELLFLAGS := /C
  PROM_FILE := $(CURDIR)\configs\prometheus.yml
  CONFIG_FILE := configs\config.yaml
  FILE_CHECK := if not exist configs\prometheus.yml ( echo configs\prometheus.yml not found & exit 1 )
  RM_PROM := -@docker rm -f prometheus >NUL 2>&1
  RM_GRAF := -@docker rm -f grafana >NUL 2>&1
  # Extract ports from config.yaml (fallback to 9090/3000)
  PROM_PORT := $(shell powershell -NoProfile -Command "$$c=Get-Content -LiteralPath '$(CONFIG_FILE)' -Raw; if ($$c -match 'prometheus_port:\s*([0-9]+)') { $$matches[1] } else { 9090 }")
  GRAFANA_PORT := $(shell powershell -NoProfile -Command "$$c=Get-Content -LiteralPath '$(CONFIG_FILE)' -Raw; if ($$c -match 'grafana_port:\s*([0-9]+)') { $$matches[1] } else { 3000 }")
else
  SHELL := /bin/sh
  .SHELLFLAGS := -c
  PROM_FILE := $${PWD}/configs/prometheus.yml
  CONFIG_FILE := configs/config.yaml
  FILE_CHECK := if [ ! -f configs/prometheus.yml ]; then echo "configs/prometheus.yml not found"; exit 1; fi
  RM_PROM := -@docker rm -f prometheus >/dev/null 2>&1 || true
  RM_GRAF := -@docker rm -f grafana >/dev/null 2>&1 || true
  # Extract ports from config.yaml (fallback to 9090/3000)
  PROM_PORT := $(shell awk -F: '/^[[:space:]]*prometheus_port:/ {gsub(/[[:space:]]/,"",$$2); print $$2; found=1; exit} END{if(!found) print 9090}' $(CONFIG_FILE))
  GRAFANA_PORT := $(shell awk -F: '/^[[:space:]]*grafana_port:/ {gsub(/[[:space:]]/,"",$$2); print $$2; found=1; exit} END{if(!found) print 3000}' $(CONFIG_FILE))
endif

.PHONY: build test clean run deps
deps:
	go get github.com/prometheus/client_golang/prometheus
	go get github.com/prometheus/client_golang/prometheus/promhttp
	go get gopkg.in/yaml.v3

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
	docker run -d --name=prometheus -p $(PROM_PORT):9090 -v "$(PROM_FILE)":/etc/prometheus/prometheus.yml prom/prometheus
	docker run -d -p $(GRAFANA_PORT):3000 --name=grafana grafana/grafana-oss

stop-metrics:
	$(RM_PROM)
	$(RM_GRAF)

.PHONY: test_with_metrics
test-with-metrics:
	@echo ">> Generating comprehensive traffic and verifying metrics (override PROXY_ADDR/UPSTREAM_ADDR as needed; defaults https://localhost:8090 and http://localhost:9000)"
	go test -v ./internal/e2e -count=1 -parallel=1 -p=1
