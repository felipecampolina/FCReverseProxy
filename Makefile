# Makefile for traefik-challenge-2

# Cross-platform shell and helpers for metrics
ifeq ($(OS),Windows_NT)
  SHELL := cmd.exe
  .SHELLFLAGS := /C
  PROM_FILE := $(CURDIR)\configs\prometheus.yml
  CONFIG_FILE := configs\config.yaml
  # Add upstream config path
  UPSTREAM_CONFIG_FILE := $(CURDIR)\configs\config-upstream.yaml
  # Also ensure Loki and Promtail configs exist
  FILE_CHECK := if not exist configs\prometheus.yml ( echo configs\prometheus.yml not found & exit 1 ) & if not exist configs\loki-config.yaml ( echo configs\loki-config.yaml not found & exit 1 ) & if not exist configs\promtail-config.yaml ( echo configs\promtail-config.yaml not found & exit 1 ) & if not exist configs\config-upstream.yaml ( echo configs\config-upstream.yaml not found & exit 1 )
  RM_PROM := -@docker rm -f prometheus >NUL 2>&1
  RM_GRAF := -@docker rm -f grafana >NUL 2>&1
  RM_LOKI := -@docker rm -f loki >NUL 2>&1
  RM_PROMTAIL := -@docker rm -f promtail >NUL 2>&1
  # Extract ports from config.yaml (fallback to 9090/3000)
  PROM_PORT := $(shell powershell -NoProfile -Command "$$c=Get-Content -LiteralPath '$(CONFIG_FILE)' -Raw; if ($$c -match 'prometheus_port:\s*([0-9]+)') { $$matches[1] } else { 9090 }")
  GRAFANA_PORT := $(shell powershell -NoProfile -Command "$$c=Get-Content -LiteralPath '$(CONFIG_FILE)' -Raw; if ($$c -match 'grafana_port:\s*([0-9]+)') { $$matches[1] } else { 3000 }")
  # Loki host port from config.yaml (fallback to 3100)
  LOKI_FILE := $(CURDIR)\configs\loki-config.yaml
  PROMTAIL_FILE := $(CURDIR)\configs\promtail-config.yaml
  LOKI_PORT := $(shell powershell -NoProfile -Command "$$c=Get-Content -LiteralPath '$(CONFIG_FILE)' -Raw; if ($$c -match 'loki_port:\s*([0-9]+)') { $$matches[1] } else { 3100 }")
  # Proxy listen port from config.yaml (fallback to 8090)
  PROXY_PORT := $(shell powershell -NoProfile -Command "$$c=Get-Content -LiteralPath '$(CONFIG_FILE)' -Raw; if ($$c -match 'listen:\s*\"?\s*:(\d+)') { $$matches[1] } else { 8090 }")
  # Safe port fallbacks (if extracted values end up empty)
  SAFE_PROM_PORT := $(if $(strip $(PROM_PORT)),$(PROM_PORT),9090)
  SAFE_GRAFANA_PORT := $(if $(strip $(GRAFANA_PORT)),$(GRAFANA_PORT),3000)
  SAFE_LOKI_PORT := $(if $(strip $(LOKI_PORT)),$(LOKI_PORT),3100)
  SAFE_PROXY_PORT := $(if $(strip $(PROXY_PORT)),$(PROXY_PORT),8090)
  # Upstream host port flags from upstream.listen (fallback to 9000..9004)
  UPSTREAM_PORT_FLAGS := $(shell powershell -NoProfile -Command "$$p=@(); if (Test-Path '$(UPSTREAM_CONFIG_FILE)') { $$c=Get-Content -LiteralPath '$(UPSTREAM_CONFIG_FILE)' -Raw; $$ms = [regex]::Matches($$c, ':\s*([0-9]+)'); foreach ($$m in $$ms) { $$p += [int]$$m.Groups[1].Value }; $$p = $$p | Sort-Object -Unique } else { $$p = 9000,9001,9002,9003,9004 }; ( $$p | ForEach-Object { '-p ' + $$_ + ':' + $$_ } ) -join ' '")
  # First upstream port (fallback to 9000)
  FIRST_UPSTREAM_PORT := $(shell powershell -NoProfile -Command "if (Test-Path '$(UPSTREAM_CONFIG_FILE)') { $$c=Get-Content -LiteralPath '$(UPSTREAM_CONFIG_FILE)' -Raw; $$m=[regex]::Matches($$c, ':\s*([0-9]+)'); if ($$m.Count -gt 0) { $$m[0].Groups[1].Value } else { 9000 } } else { 9000 }")
  METRICS_NET := metrics
  NET_CREATE := -@docker network inspect $(METRICS_NET) >NUL 2>&1 || docker network create $(METRICS_NET) >NUL
  GRAFANA_PROV := $(CURDIR)/configs/grafana/provisioning
  GRAFANA_DASH := $(CURDIR)/internal/metrics/dashboards

  # --- App containers (proxy + upstream) ---
  APP_NET := app
  NET_CREATE_APP := -@docker network inspect $(APP_NET) >NUL 2>&1 || docker network create $(APP_NET) >NUL
  RM_PROXY := -@docker rm -f proxy >NUL 2>&1
  RM_UPSTREAM := -@docker rm -f upstream >NUL 2>&1
  DOCKER_GO := golang:1.25-alpine
  MOUNT_CUR := $(CURDIR)
else
  SHELL := /bin/sh
  .SHELLFLAGS := -c
  PROM_FILE := $${PWD}/configs/prometheus.yml
  CONFIG_FILE := configs/config.yaml
  # Add upstream config path
  UPSTREAM_CONFIG_FILE := configs/config-upstream.yaml
  # Also ensure Loki and Promtail configs exist
  FILE_CHECK := if [ ! -f configs/prometheus.yml ]; then echo "configs/prometheus.yml not found"; exit 1; fi; if [ ! -f configs/loki-config.yaml ]; then echo "configs/loki-config.yaml not found"; exit 1; fi; if [ ! -f configs/promtail-config.yaml ]; then echo "configs/promtail-config.yaml not found"; exit 1; fi
  RM_PROM := -@docker rm -f prometheus >/dev/null 2>&1 || true
  RM_GRAF := -@docker rm -f grafana >/dev/null 2>&1 || true
  RM_LOKI := -@docker rm -f loki >/dev/null 2>&1 || true
  RM_PROMTAIL := -@docker rm -f promtail >/dev/null 2>&1 || true
  # Extract ports from config.yaml (fallback to 9090/3000)
  PROM_PORT := $(shell awk -F: '/^[[:space:]]*prometheus_port:/ {gsub(/[[:space:]]/,"",$$2); print $$2; found=1; exit} END{if(!found) print 9090}' $(CONFIG_FILE))
  GRAFANA_PORT := $(shell awk -F: '/^[[:space:]]*grafana_port:/ {gsub(/[[:space:]]/,"",$$2); print $$2; found=1; exit} END{if(!found) print 3000}' $(CONFIG_FILE))
  # Loki host port from config.yaml (fallback to 3100)
  LOKI_FILE := $${PWD}/configs/loki-config.yaml
  PROMTAIL_FILE := $${PWD}/configs/promtail-config.yaml
  LOKI_PORT := $(shell awk -F: '/^[[:space:]]*loki_port:/ {gsub(/[[:space:]]/,"",$$2); print $$2; found=1; exit} END{if(!found) print 3100}' $(CONFIG_FILE))
  # Proxy listen port from config.yaml (fallback to 8090)
  PROXY_PORT := $(shell awk '/^[[:space:]]*listen:[[:space:]]*\"/ { if (match($$0, /:[[:space:]]*([0-9]+)/, a)) { print a[1]; found=1; exit } } END{ if(!found) print 8090 }' $(CONFIG_FILE))
  # Safe port fallbacks (if extracted values end up empty, e.g., BSD awk issues)
  SAFE_PROM_PORT := $(if $(strip $(PROM_PORT)),$(PROM_PORT),9090)
  SAFE_GRAFANA_PORT := $(if $(strip $(GRAFANA_PORT)),$(GRAFANA_PORT),3000)
  SAFE_LOKI_PORT := $(if $(strip $(LOKI_PORT)),$(LOKI_PORT),3100)
  SAFE_PROXY_PORT := $(if $(strip $(PROXY_PORT)),$(PROXY_PORT),8090)
  # Upstream host port flags from upstream.listen (fallback to 9000..9004)
  UPSTREAM_PORT_FLAGS := $(shell sh -c "ports=$$(grep -oE ':[[:space:]]*[0-9]+' $(UPSTREAM_CONFIG_FILE) 2>/dev/null | sed -E 's/[^0-9]*([0-9]+)/\\1/' | sort -n | uniq); if [ -z \"$$ports\" ]; then ports='9000 9001 9002 9003 9004'; fi; for p in $$ports; do printf -- '-p %s:%s ' $$p $$p; done")
  # First upstream port (fallback to 9000)
  FIRST_UPSTREAM_PORT := $(shell sh -c "p=$$(grep -oE ':[[:space:]]*[0-9]+' $(UPSTREAM_CONFIG_FILE) 2>/dev/null | sed -E 's/[^0-9]*([0-9]+)/\\1/' | head -n1); if [ -z \"$$p\" ]; then echo 9000; else echo $$p; fi")
  METRICS_NET := metrics
  NET_CREATE := -@docker network inspect $(METRICS_NET) >/dev/null 2>&1 || docker network create $(METRICS_NET) >/dev/null
  GRAFANA_PROV := $(CURDIR)/configs/grafana/provisioning
  GRAFANA_DASH := $(CURDIR)/internal/metrics/dashboards

  # --- App containers (proxy + upstream) ---
  APP_NET := app
  NET_CREATE_APP := -@docker network inspect $(APP_NET) >/dev/null 2>&1 || docker network create $(APP_NET) >/dev/null
  RM_PROXY := -@docker rm -f proxy >/dev/null 2>&1 || true
  RM_UPSTREAM := -@docker rm -f upstream >/dev/null 2>&1 || true
  DOCKER_GO := golang:1.25-alpine
  MOUNT_CUR := $(CURDIR)
endif

# Ensure the right Go toolchain is used locally (auto-download if needed)
export GOTOOLCHAIN=auto

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
	go test -count=1 ./test/unit -v

clean:
	rm -rf bin/

# Keep dev runs using golang image (config-driven ports)
.PHONY: run-proxy run-upstream run-demo stop-proxy stop-upstream stop-demo
run-proxy:
	$(NET_CREATE_APP)
	$(RM_PROXY)
	docker run -d --name=proxy --network $(APP_NET) -p $(SAFE_PROXY_PORT):$(SAFE_PROXY_PORT) -v "$(MOUNT_CUR)":/app -w /app $(DOCKER_GO) sh -c "go run -mod=mod ./cmd/server"
	$(NET_CREATE)
	docker network connect $(METRICS_NET) proxy

run-upstream:
	$(NET_CREATE_APP)
	$(RM_UPSTREAM)
	docker run -d --name=upstream --network $(APP_NET) $(UPSTREAM_PORT_FLAGS) -v "$(MOUNT_CUR)":/app -w /app $(DOCKER_GO) sh -c "go run -mod=mod ./cmd/upstream"
	$(NET_CREATE)
	docker network connect $(METRICS_NET) upstream

# Run both containers (start upstream first, then proxy)
run-demo: run-upstream run-proxy

stop-proxy:
	$(RM_PROXY)

stop-upstream:
	$(RM_UPSTREAM)

stop-demo: stop-proxy stop-upstream

# --- Metrics stack (Prometheus + Grafana + Loki + Promtail) ---
.PHONY: run-metrics stop-metrics
run-metrics:
	$(FILE_CHECK)
	$(RM_PROM)
	$(RM_GRAF)
	$(RM_LOKI)
	$(RM_PROMTAIL)
	$(NET_CREATE)
	docker run -d --name=prometheus --network $(METRICS_NET) -p $(SAFE_PROM_PORT):9090 -v "$(PROM_FILE)":/etc/prometheus/prometheus.yml prom/prometheus
	docker run -d --name=loki --network $(METRICS_NET) -p $(SAFE_LOKI_PORT):3100 -v "$(LOKI_FILE)":/etc/loki/local-config.yaml grafana/loki -config.file=/etc/loki/local-config.yaml
	docker run -d --name=promtail --network $(METRICS_NET) -v "$(PROMTAIL_FILE)":/etc/promtail/config.yml grafana/promtail -config.file=/etc/promtail/config.yml
	docker run -d --name=grafana --network $(METRICS_NET) -p $(SAFE_GRAFANA_PORT):3000 -v "$(GRAFANA_PROV)":/etc/grafana/provisioning -v "$(GRAFANA_DASH)":/var/lib/grafana/dashboards -e GF_AUTH_ANONYMOUS_ENABLED=true -e GF_AUTH_ANONYMOUS_ORG_ROLE=Admin -e GF_SECURITY_ADMIN_PASSWORD=admin grafana/grafana:latest

stop-metrics:
	$(RM_PROM)
	$(RM_GRAF)
	$(RM_LOKI)
	$(RM_PROMTAIL)

.PHONY: test_with_metrics
test-with-metrics:
	@echo ">> Generating comprehensive traffic and verifying metrics (override PROXY_ADDR/UPSTREAM_ADDR as needed; defaults https://localhost:8090 and http://localhost:9000)"
	go test -v ./test/e2e -count=1 -parallel=1 -p=1
