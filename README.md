# FCReverseProxy

## Overview
FCReverseProxy is a **lightweight, high-performance reverse proxy** written in Go. It includes built-in **caching, load balancing, health checking, and monitoring** capabilities. Designed for **scalability and observability**, it can handle **production-level traffic** while providing insights into system performance.

---

## Table of Contents
- [Features](#features)
  - [Caching](#caching)
  - [Load Balancer](#load-balancer)
  - [Health Checker](#health-checker)
  - [TLS Termination](#tls-termination)
  - [Request Queue](#request-queue)
  - [Unit Tests](#unit-tests)
  - [Metrics & Dashboard](#metrics--dashboard)
  - [Logging](#logging)
- [Prerequisites](#prerequisites)
- [Quick Start (Makefile Commands)](#quick-start-makefile-commands)
  - [Core Commands](#core-commands)
  - [Docker-based Commands](#docker-based-commands)
  - [Monitoring Stack](#monitoring-stack)
  - [Recommended: Example Demo Commands](#recommended-example-demo-commands)
- [System Components (Localhost Services)](#system-components-localhost-services)
- [Proxy Routes (Exposed by FCReverseProxy)](#proxy-routes-exposed-by-fcreverseproxy)
- [Upstream Demo Routes (Backend Simulation)](#upstream-demo-routes-backend-simulation)
- [Testing](#testing)
  - [Unit Tests](#unit-tests-1)
  - [End-to-End Testing](#end-to-end-testing)
- [Testing with curl](#testing-with-curl)
  - [HTTP Testing](#http-testing)
  - [HTTPS Testing](#https-testing)
  - [Important Note on Certificates](#important-note-on-certificates)
- [Monitoring](#monitoring)
  - [Dashboards Overview](#dashboards-overview)
    - [Reverse Proxy Dashboard](#reverse-proxy-dashboard)
    - [Upstream Dashboard](#upstream-dashboard)
  - [Troubleshooting](#troubleshooting)
- [Next Steps & Improvements](#next-steps--improvements)
- [Acknowledgments](#acknowledgments)

---

## Features

### Caching
- In-memory LRU Cache: Improves response times and reduces backend load.
- Configurable cache size.

### Load Balancer
- Round Robin: Distributes requests evenly across backends.
- Least Connections: Routes new requests to the backend with the fewest active connections.

### Health Checker
- Periodically checks the health of backend servers.
- Automatically removes unhealthy backends from the rotation.
- Reintroduces backends when they recover.

### TLS Termination
- Accepts HTTPS client requests and forwards them as HTTP requests to backend servers.
- Simplifies backend configuration by offloading TLS.
- Supports certificate configuration for secure connections.

### Request Queue
- Queues incoming requests during high load.
- Prevents backend overload by applying backpressure.
- Configurable maximum queue size.

### Unit Tests
- Comprehensive unit test coverage for core modules.
- Ensures reliability and correctness of critical components.

### Metrics & Dashboard
- Exposes Prometheus metrics for:
  - Request count
  - Latency
  - Cache hits/misses
  - Active connections
- Load Balancer Visualization
- Pre-configured Grafana dashboard for real-time monitoring and visualization.

### Logging
- Integrated logging system with Loki and Promtail.
- Centralized log collection for debugging and observability.
- Structured logs for easier querying and filtering.

---

## Prerequisites
- **Docker** (all dependencies will be automatically downloaded and configured using make commands, which run Docker containers).

---

## Quick Start (Makefile Commands)
The project provides a Makefile to simplify building, running, and testing. While you can run the proxy directly with `make run`, it is **recommended** to use the Docker-based commands for a consistent environment.

### Core Commands
ALERT: For development only. Prefer the Docker-based commands.
- `make deps`  
  Installs required Go dependencies (Prometheus client, PromHTTP, YAML parser).

- `make run`  
  Runs the reverse proxy locally with Go (not containerized).  

- `make build`  
  Compiles the project and creates executables in the `bin/` directory:
  - `bin/server` → Reverse proxy binary
  - `bin/upstream` → Example upstream backend service

- `make test`  
  Runs the unit tests located in `test/unit`.

- `make clean`  
  Removes build artifacts (`bin/` directory).

### Docker-based Commands
- `make run-proxy`  
  Runs the reverse proxy inside a Docker container.
  - Creates the required Docker network.
  - Binds the configured proxy port (default: **8090**).

- `make run-upstream`  
  Runs the upstream backend service in a Docker container.
  - Uses ports from `configs/config-upstream.yaml` (default: **9000–9005**).

- `make run-demo`  
  Starts both the upstream service and the proxy (full working demo).

- `make stop-proxy`, `make stop-upstream`, `make stop-demo`  
  Stops and removes the corresponding containers.

### Monitoring Stack
- `make run-metrics`  
  Starts the monitoring stack (Prometheus, Grafana, Loki, Promtail) inside Docker containers.
  - Prometheus → **http://localhost:9090**
  - Grafana → **http://localhost:3000**
  - Loki → **http://localhost:3100**

- `make stop-metrics`  
  Stops all monitoring containers.

### Recommended: Example Demo Commands
This demo runs a complete local environment using Docker:
- What it starts:
  - FCReverseProxy (the proxy) using configs/config.yaml
  - A simple upstream demo service with multiple instances/ports from configs/config-upstream.yaml
- Pre-set configs (modifiable in configs/):
  - Proxy listens on http://localhost:8090
  - Upstreams servers on ports 9000–9005
  - Load balancing: round_robin by default (changeable to least_connections)
  - In-memory cache, request queue, and health checks enabled with sensible defaults
  - TLS is enabled
- What the proxy does in the demo:
1. A client connects to **`https://localhost:8090`**.  
2. The proxy **terminates TLS** (uses `server.crt`/`server.key` or auto self-signed for local dev).  
3. It **checks method** (must be one of `GET, HEAD, POST, PUT, PATCH, DELETE`).  
4. It applies **backpressure rules** (concurrency + queue).  
5. It **selects a healthy upstream** using the **load balancer** (default **round-robin**, optional **least-connections**).  
6. It **forwards** the request as **HTTP** to the chosen `http://upstream:PORT`.  
7. If **cache is enabled** and the response is cacheable, it serves from/stores to in-memory cache.  
8. It returns the upstream’s response to the client.

To quickly start a full demo environment with monitoring:
```bash
# Start proxy + upstream services (creates the required Docker network)
make run-demo

# In a separate terminal, start the monitoring stack (Prometheus, Grafana, Loki, Promtail)
make run-metrics
```

---

## System Components (Localhost Services)
| Service   | URL / Port             | Description                                                  |
|-----------|-------------------------|--------------------------------------------------------------|
| FCProxy   | **http://localhost:8090**  | Reverse proxy entrypoint (client requests go here).         |
| Prometheus| **http://localhost:9090**  | Collects metrics exposed by the proxy.                      |
| Grafana   | **http://localhost:3000**  | Dashboard visualization (uses Prometheus + Loki data).      |
| Loki      | **http://localhost:3100**  | Centralized log storage.                                    |
| Promtail  | (local agent)           | Collects and ships proxy logs to Loki.                      |

---

## Proxy Routes (Exposed by FCReverseProxy)
| Path       | Methods | Description                                                                                                     |
|------------|---------|-----------------------------------------------------------------------------------------------------------------|
| /          | ALL     | Main reverse proxy entrypoint. Forwards requests to upstream backends (with caching, load balancing, and queueing). |
| /metrics   | GET     | Prometheus metrics endpoint (requests, latency, cache hits/misses, active connections).                         |
| /healthz   | GET     | Health check endpoint for the proxy itself (returns 200 OK if healthy).                                         |

---

## Upstream Demo Routes (Backend Simulation)
| Path             | Methods | Description                                |
|------------------|---------|--------------------------------------------|
| /api/items       | GET     | List all items (JSON response).            |
| /api/items       | POST    | Create a new item (accepts JSON body).     |
| /api/items/{id}  | GET     | Retrieve a single item by ID.              |
| /api/items/{id}  | PUT     | Update an item by ID.                      |
| /api/items/{id}  | DELETE  | Delete an item by ID.                      |
| /metrics         | GET     | Prometheus metrics for the upstream service. |
| /healthz         | GET     | Health check endpoint for the upstream.    |

---

## Testing

### Unit Tests
Run unit tests without requiring Docker or additional environment setup:
```bash
make test
```
- Executes tests located in test/unit.

### End-to-End Testing
Perform end-to-end tests in the demo environment:
```bash
make run-demo
make run-metrics
make test-with-metrics
```
- Ensure the demo environment is running before executing these commands.

---

## Testing with curl

### HTTP Testing
Test the proxy and its endpoints over HTTP:
```bash
# Test proxy forwarding
curl http://localhost:8090/api/items

# Test health endpoint
curl http://localhost:8090/healthz

# Test metrics exposure
curl http://localhost:8090/metrics

```

### HTTPS Testing
Test the proxy and its endpoints over HTTPS:
```bash
# Test proxy forwarding with HTTPS
curl --cacert server.crt https://localhost:8090/api/items

# Test health endpoint with HTTPS
curl --cacert server.crt https://localhost:8090/healthz

# Test metrics exposure with HTTPS
curl --cacert server.crt https://localhost:8090/metrics

```

### Important Note on Certificates
In this demo, a static certificate (`server.crt`) and key are automatically generated **for demonstration purposes only**. This approach is **strictly for testing and should never be used in a production environment**.
- Ensure the `server.crt` certificate is correctly configured for secure connections.

---

## Monitoring
A ready-to-run monitoring stack (Prometheus, Grafana, Loki, Promtail) is included with pre-provisioned dashboards. Use it to see traffic, latency, cache efficiency, queue behavior, and upstream health at a glance.

Step-by-step (after starting the stack):
1) Start demo services (proxy + upstream)  
   `make run-demo`
2) Start the monitoring stack  
   `make run-metrics`
3) Generate some traffic so charts populate

4) Open Grafana (anonymous access enabled)
- **http://localhost:3000** (or your configured Grafana port)
- Dashboards:
  - Reverse Proxy — Overview: **http://localhost:3000/d/proxy-overview**
  - Upstream — Overview: **http://localhost:3000/d/upstream-overview**
- Logs:
  - Navigate to Drilldown → Select Logs.
  
  ### Dashboards Overview

  #### Reverse Proxy Dashboard
  This dashboard provides insights into the performance and behavior of the reverse proxy. Key metrics include:
  - **Total requests**: The number of responses the proxy has served. Use filters (method/status/cache) to slice the view.
  - **Total cache hits**: The frequency of responses served from cache. A higher value indicates reduced backend load.
  - **Total errors (4xx/5xx)**: Client and server errors observed by the proxy. Spikes may indicate issues.
  - **Cache hit ratio (%)**: The percentage of requests served from cache, useful for tracking cache effectiveness.
  - **Current RPS**: Live throughput through the proxy (requests per second).
  - **RPS by status (time series)**: Traffic over time, broken down by response code class (200, 404, 500, etc.).
  - **Latency p50/p90/p99**: End-to-end response times (median, tail, and worst-case).
  - **RPS by method**: Traffic over time by HTTP method (GET, POST, etc.).
  - **RPS by cache outcome**: Traffic categorized as HIT/MISS/BYPASS over time.
  - **Queue depth**: The number of requests waiting to be processed. Sustained growth signals saturation.
  - **Queue rejections (rate)**: Requests dropped due to a full queue. This should ideally be near zero.
  - **Queue timeouts (rate)**: Requests that timed out while waiting in the queue. Investigate if non-zero.
  - **Queue wait p50/p90/p99**: Time requests spend in the queue before execution. Rising values indicate pressure.
  - **Upstream RPS by X-Upstream**: Traffic distribution across backends.
  - **Upstream latency p50/p90/p99 by X-Upstream**: Backend-specific response times as seen by the proxy.
  - **Total requests by X-Upstream (bar)**: Cumulative request volume per backend to identify imbalances.
  - **Reverse proxy up**: A simple indicator showing whether Grafana/Prometheus is scraping the proxy (1 = up).

  #### Upstream Dashboard
  This dashboard focuses on the performance and health of the upstream backend services. Key metrics include:
  - **Total upstream requests**: The total number of requests handled by the backend.
  - **Total upstream errors (4xx/5xx)**: Errors returned by the backend. Watch for spikes.
  - **Inflight (current)**: The number of requests currently being processed by the backend.
  - **Current RPS**: Live throughput handled by the backend.
  - **RPS by status (time series)**: Backend traffic over time, split by response code class.
  - **Latency p50/p90/p99**: Backend request processing times (median and tail).
  - **Inflight (time series)**: Concurrency trends over time. Rising concurrency alongside rising latency may indicate overload.

### Troubleshooting
- Empty panels? Wait ~10–20s for the first Prometheus scrape and generate traffic.
- Ports differ? Edit `configs/config.yaml`; `make run-metrics` respects configured ports.
- Logs not visible? To view logs in the Grafana dashboard, you may need to refresh the page in the Drilldown section. This ensures that the logs collected by Promtail are visible.

---

## Next Steps & Improvements

- **Rate Limiting**: Implement global, per-IP, and per-key throttling to prevent abuse and ensure fair usage.  
- **Group-Based Scaling**: Introduce request quotas and scheduling mechanisms by group or tenant to support multi-tenant environments.  
- **DDoS Mitigation**: Add dedicated protection strategies beyond basic rate limits for stronger resilience against attacks.  
- **Advanced Caching**: Enhance caching with smarter algorithms and policies (e.g., LRU, LFU, adaptive TTLs) to improve performance.  
- **TLS/SSL Passthrough**: Support passthrough for encrypted traffic to improve flexibility in secure deployments.  
- **Protocol Support**: Extend compatibility to WebSockets, UDP, and TCP for broader application coverage.  
- **Web Application Firewall (WAF)**: Introduce a baseline set of rules to detect and block malicious patterns and requests.  



---

## Acknowledgments
Special thanks to the following technologies and teams that made this project possible:

- Go (Golang) → Core language used to build the reverse proxy.
- Prometheus → Metrics collection and monitoring system.
- Grafana → Visualization and dashboards for metrics and logs.
- Loki & Promtail → Centralized logging system integrated with Grafana.
- Docker → Containerization for consistent local development and demo environments.
- Makefile → Simplified build, test, and run workflows.
- Traefik Team → For inspiring and providing this challenge.

And to the open-source community for providing libraries, best practices, and inspiration.
