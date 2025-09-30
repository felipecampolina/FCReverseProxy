# Makefile for traefik-challenge-2

# Cross-platform shell and helpers for metrics
ifeq ($(OS),Windows_NT)
  SHELL := cmd.exe
  .SHELLFLAGS := /C
  PROM_FILE := $(CURDIR)\configs\prometheus.yml
  CONFIG_FILE := configs\config.yaml
  # Also ensure Loki and Promtail configs exist
  FILE_CHECK := if not exist configs\prometheus.yml ( echo configs\prometheus.yml not found & exit 1 ) & if not exist configs\loki-config.yaml ( echo configs\loki-config.yaml not found & exit 1 ) & if not exist configs\promtail-config.yaml ( echo configs\promtail-config.yaml not found & exit 1 )
  RM_PROM := -@docker rm -f prometheus >NUL 2>&1
  RM_GRAF := -@docker rm -f grafana >NUL 2>&1
  RM_LOKI := -@docker rm -f loki >NUL 2>&1
  RM_PROMTAIL := -@docker rm -f promtail >NUL 2>&1
  # Extract ports from config.yaml (fallback to 9090/3000)
  PROM_PORT := $(shell powershell -NoProfile -Command "$$c=Get-Content -LiteralPath '$(CONFIG_FILE)' -Raw; if ($$c -match 'prometheus_port:\s*([0-9]+)') { $$matches[1] } else { 9090 }")
  GRAFANA_PORT := $(shell powershell -NoProfile -Command "$$c=Get-Content -LiteralPath '$(CONFIG_FILE)' -Raw; if ($$c -match 'grafana_port:\s*([0-9]+)') { $$matches[1] } else { 3000 }")
  # Loki: config path and port (fallback to 3100)
  LOKI_FILE := $(CURDIR)\configs\loki-config.yaml
  PROMTAIL_FILE := $(CURDIR)\configs\promtail-config.yaml
  LOKI_PORT := $(shell powershell -NoProfile -Command "$$c=Get-Content -LiteralPath '$(LOKI_FILE)' -Raw; if ($$c -match 'http_listen_port:\s*([0-9]+)') { $$matches[1] } else { 3100 }")
  METRICS_NET := metrics
  NET_CREATE := -@docker network inspect $(METRICS_NET) >NUL 2>&1 || docker network create $(METRICS_NET) >NUL
  GRAFANA_PROV := $(CURDIR)/configs/grafana/provisioning
  GRAFANA_DASH := $(CURDIR)/internal/metrics/dashboards
else
  SHELL := /bin/sh
  .SHELLFLAGS := -c
  PROM_FILE := $${PWD}/configs/prometheus.yml
  CONFIG_FILE := configs/config.yaml
  # Also ensure Loki and Promtail configs exist
  FILE_CHECK := if [ ! -f configs/prometheus.yml ]; then echo "configs/prometheus.yml not found"; exit 1; fi; if [ ! -f configs/loki-config.yaml ]; then echo "configs/loki-config.yaml not found"; exit 1; fi; if [ ! -f configs/promtail-config.yaml ]; then echo "configs/promtail-config.yaml not found"; exit 1; fi
  RM_PROM := -@docker rm -f prometheus >/dev/null 2>&1 || true
  RM_GRAF := -@docker rm -f grafana >/dev/null 2>&1 || true
  RM_LOKI := -@docker rm -f loki >/dev/null 2>&1 || true
  RM_PROMTAIL := -@docker rm -f promtail >/dev/null 2>&1 || true
  # Extract ports from config.yaml (fallback to 9090/3000)
  PROM_PORT := $(shell awk -F: '/^[[:space:]]*prometheus_port:/ {gsub(/[[:space:]]/,"",$$2); print $$2; found=1; exit} END{if(!found) print 9090}' $(CONFIG_FILE))
  GRAFANA_PORT := $(shell awk -F: '/^[[:space:]]*grafana_port:/ {gsub(/[[:space:]]/,"",$$2); print $$2; found=1; exit} END{if(!found) print 3000}' $(CONFIG_FILE))
  # Loki: config path and port (fallback to 3100)
  LOKI_FILE := $${PWD}/configs/loki-config.yaml
  PROMTAIL_FILE := $${PWD}/configs/promtail-config.yaml
  LOKI_PORT := $(shell awk -F: '/^[[:space:]]*http_listen_port:/ {gsub(/[[:space:]]/,"",$$2); print $$2; found=1; exit} END{if(!found) print 3100}' $(LOKI_FILE))
  METRICS_NET := metrics
  NET_CREATE := -@docker network inspect $(METRICS_NET) >/dev/null 2>&1 || docker network create $(METRICS_NET) >/dev/null
  GRAFANA_PROV := $(CURDIR)/configs/grafana/provisioning
  GRAFANA_DASH := $(CURDIR)/internal/metrics/dashboards
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
	go test ./test -v

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

# --- Metrics stack (Prometheus + Grafana + Loki + Promtail) ---
.PHONY: run-metrics stop-metrics
run-metrics:
	$(FILE_CHECK)
	$(RM_PROM)
	$(RM_GRAF)
	$(RM_LOKI)
	$(RM_PROMTAIL)
	$(NET_CREATE)
	docker run -d --name=prometheus --network $(METRICS_NET) -p $(PROM_PORT):9090 -v "$(PROM_FILE)":/etc/prometheus/prometheus.yml prom/prometheus
	docker run -d --name=loki --network $(METRICS_NET) -p $(LOKI_PORT):3100 -v "$(LOKI_FILE)":/etc/loki/local-config.yaml grafana/loki -config.file=/etc/loki/local-config.yaml
	docker run -d --name=promtail --network $(METRICS_NET) -v "$(PROMTAIL_FILE)":/etc/promtail/config.yml -v /var/log:/var/log:ro grafana/promtail -config.file=/etc/promtail/config.yml
	docker run -d --name=grafana --network $(METRICS_NET) -p $(GRAFANA_PORT):3000 -v "$(GRAFANA_PROV)":/etc/grafana/provisioning -v "$(GRAFANA_DASH)":/var/lib/grafana/dashboards -e GF_AUTH_ANONYMOUS_ENABLED=true -e GF_AUTH_ANONYMOUS_ORG_ROLE=Admin -e GF_SECURITY_ADMIN_PASSWORD=admin grafana/grafana:latest

stop-metrics:
	$(RM_PROM)
	$(RM_GRAF)
	$(RM_LOKI)
	$(RM_PROMTAIL)

.PHONY: test_with_metrics
test-with-metrics:
	@echo ">> Generating comprehensive traffic and verifying metrics (override PROXY_ADDR/UPSTREAM_ADDR as needed; defaults https://localhost:8090 and http://localhost:9000)"
	go test -v ./test/e2e -count=1 -parallel=1 -p=1
