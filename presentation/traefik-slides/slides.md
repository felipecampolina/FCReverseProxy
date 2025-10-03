---
# try also 'default' to start simple
theme: seriph
# random image from a curated Unsplash collection by Anthony
# like them? see https://unsplash.com/collections/94734566/slidev
background: https://upload.wikimedia.org/wikipedia/commons/c/cc/Digital_rain_animation_medium_letters_shine.gif
# some information about your slides (markdown enabled)
title: FCReverseProxy-presentation
info: |
  ## Slidev Starter Template
  Presentation slides for developers.

  Learn more at [Sli.dev](https://sli.dev)
# apply UnoCSS classes to the current slide
class: text-center
# https://sli.dev/features/drawing
drawings:
  persist: false
# slide transition: https://sli.dev/guide/animations.html#slide-transitions
transition: slide-left
# enable MDC Syntax: https://sli.dev/features/mdc
mdc: true
---

# FCReverseProxy

### Made by Felipe Campolina Soares de Paula



<div class="abs-br m-6 text-xl">
  <a href="https://github.com/felipecampolina/FCReverseProxy/tree/main" target="_blank" class="slidev-icon-btn">
    <carbon:logo-github />
  </a>
</div>

<!--
The last comment block of each slide will be treated as slide notes. It will be visible and editable in Presenter Mode along with the slide. [Read more in the docs](https://sli.dev/guide/syntax.html#notes)
-->

---
layout: two-cols
layoutClass: gap-16
---

# Table of contents

<Toc text-sm minDepth="1" maxDepth="5" />


---
theme: default
layout: center
class: text-center
---

# About Myself

👨‍💻 **Felipe Campolina**  
*Software Engineer / Full Stack Developer*  

---

##  Experience
- 🚀 **Full Stack Developer @ Biobyte Brasil** *(Feb 2024 – Present)*  
  *Angular · Node.js · Express.js · TypeScript · SQL · MongoDB · AWS (EC2, S3, Lambda, IAM) · GPT/LLMs · RAG Pipelines*  

- 🎓 **Web Developer Scholarship Holder @ PUCTEC** *(Jul 2023 – Dec 2023)*  
  *HTML · CSS · JavaScript · Bootstrap · PHP · Laravel · WordPress · MySQL · Git*  

- 💻 **Front-End Developer Trainee @ Abil Agency** *(May 2022 – Jun 2023)*  
  *HTML · CSS · JavaScript · Bootstrap · UI/UX · Responsive Design · SEO*  

- 🧑‍🏫 **Technical Tutor @ PUC Minas** *(Jun 2022 – Jul 2023)*  
  *C · C++ · Java · Programming Fundamentals · Algorithms*  

---

##  Education

###  B.Sc. Computer Science  
**Pontifical Catholic University of Minas Gerais (PUC Minas)**  
*(2022 – 2026)*  


###  B.Sc. Computer Engineering  
**Polytechnic Institute of Guarda (IPG – Portugal)**  
*(2025 – 2026)*  

<style>
h2 {
  color: #2B90B6;
  margin-bottom: 1.2rem;
}
h3 {
  margin-top: 1.2rem;
  color: #146b8c;
}
</style>


---
theme: default
layout: center
class: text-center
---

# Forward Proxy vs Reverse Proxy

Many businesses use proxy servers to route and secure traffic.  
But how is a **forward proxy** different from a **reverse proxy**?  

---

## Forward Proxy
- Routes traffic **from multiples clients → external system**  
- Masks client IP addresses & enforces policies  
- Regulates access, blocks unknown traffic  
- Common in businesses & data centers  

 Example: Company proxy that filters employee traffic to the Internet  


## Reverse Proxy
- Routes traffic **from client → backend servers**  
- Protects server identity from clients  
- Clients see only the reverse proxy, not the actual servers  
- Can perform **load balancing**, **SSL termination**, **firewall filtering**  

 Example: Users connect to `proxy.myapp.com` which forwards requests to multiple backend servers  

---

## Why Use a Reverse Proxy?
- Centralized **security & access control**  
- Simplifies server management (servers only accept proxy traffic)  
- Easy to **add/remove servers** without client changes  
- Improves performance via **load balancing & caching**  
<style>
h1 {
  background-color: #2B90B6;
  background-image: linear-gradient(45deg, #4EC5D4 10%, #146b8c 20%);
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
}
h2 {
  color: #2B90B6;
  margin-bottom: 1rem;
}
table {
  font-size: 0.9rem;
  margin-top: 1rem;
}
</style>

---
theme: default
layout: center
class: text-center
---

# FCReverseProxy

**My solution for the Traefik Challenge**


---

## Overview

**FCReverseProxy** is a lightweight, high-performance reverse proxy written in Go.

It is designed with scalability, resilience, and observability in mind, featuring:


<style>
table {
  font-size: 0.70rem;
  line-height: 1.1;
}
</style>

| Category | Description |
|----------|-------------|
| **Caching** | - In-memory LRU cache <br> - Configurable cache size |
| **Load Balancer** | - Round Robin and Least Connections algorithms <br> - Even traffic distribution |
| **Health Checker** | - Periodic health checks <br> - Auto removal/recovery of backends |
| **TLS Termination** | - HTTPS support with TLS offloading <br> - Configurable certificates |
| **Request Queue** | - Request queuing during high load <br> - Configurable queue size and backpressure control |
| **Unit Tests** | - Coverage for core modules <br> - Reliability and correctness validation |
| **Metrics & Dashboard** | - Prometheus metrics <br> - Grafana dashboard for live monitoring |
| **Logging** | - Structured logs via Loki and Promtail <br> - Centralized observability |

---


## Caching

- Improves response times by storing previously fetched responses in memory.
- Reduces load on backend servers by serving frequent requests from cache.
- Useful for high-read, low-change workloads (e.g., static content, APIs with repeated queries).



---

### Caching Implementation in FCReverseProxy
- **Purpose**: Reduce backend load and improve response times.
- **Type**: In-memory LRU (Least Recently Used) cache.
- **Key Features**:
  - Configurable cache size.
  - TTL (Time-to-Live) for cached entries for non cache response.
  - Cache HIT/MISS tracking.

---

### How It Works

<div class="grid grid-cols-2 gap-6 leading-relaxed">

<div>

<div class="mb-6">
  <h3>1. Cacheable Requests</h3>
  <ul>
    <li>Checked using <code>isCacheableRequest</code></li>
    <li>Skips <code>no-store</code> or <code>no-cache</code> requests</li>
    <li>Requires <code>public</code> directive with <code>Authorization</code></li>
  </ul>
</div>

<div class="mb-6">
  <h3>2. Cache Key</h3>
  <ul>
    <li>Generated with <code>buildCacheKey</code></li>
    <li>Includes method, URL, headers, optional body hash</li>
  </ul>
</div>

</div>

<div>

<div class="mb-6">
  <h3>3. Cache HIT</h3>
  <ul>
    <li>Returns valid entry if found</li>
    <li>Adds headers: <code>X-Cache: HIT</code>, <code>Age</code>, <code>X-Request-ID</code></li>
  </ul>
</div>

<div class="mb-6">
  <h3>4. Cache MISS</h3>
  <ul>
    <li>Forwards request to upstream</li>
    <li>Validates response with <code>isCacheableResponse</code></li>
    <li>Stores entry in cache with TTL</li>
  </ul>
</div>

</div>

</div>

---

###  Code Example: `isCacheableRequest`

<div class="grid grid-cols-2 gap-6 leading-relaxed">

<div>

```go
func isCacheableRequest(req *http.Request) bool {
	cacheControl := parseCacheControl(req.Header.Get("Cache-Control"))

	// Respect explicit client directives.
	if _, ok := cacheControl["no-store"]; ok {
		return false
	}
	if _, ok := cacheControl["no-cache"]; ok {
		return false
	}

	// Heuristic: avoid caching when Authorization is present
	// unless "public" is provided.
	if req.Header.Get("Authorization") != "" {
		if _, isPublic := cacheControl["public"]; !isPublic {
			return false
		}
	}
	return true
}
```

</div>

<div>

### Observation

- HTTP requests with `Authorization` headers usually **should not be cached** to avoid leaking sensitive data.
- This logic allows an exception:
  - If the client includes `Cache-Control: public`, the response **can be cached** even if it's authenticated.
- It follows a **secure-by-default policy**, requiring **explicit opt-in** for caching authenticated content.

</div>

</div>

---

###  Code Example: `buildCacheKey`

<div class="grid grid-cols-2 gap-6 leading-relaxed">

<div>

```go
func buildCacheKey(req *http.Request) string {
	keyBuilder := strings.Builder{}
	keyBuilder.WriteString(req.Method)
	keyBuilder.WriteString(" ")
	keyBuilder.WriteString(req.URL.Scheme)
	keyBuilder.WriteString("://")
	keyBuilder.WriteString(req.Host)
	keyBuilder.WriteString(singleJoiningSlash("", req.URL.Path))
	if req.URL.RawQuery != "" {
		keyBuilder.WriteString("?")
		keyBuilder.WriteString(req.URL.RawQuery)
	}
	keyBuilder.WriteString("|a=")
	keyBuilder.WriteString(strings.TrimSpace(req.Header.Get("Accept")))
	keyBuilder.WriteString("|ae=")
	keyBuilder.WriteString(strings.TrimSpace(req.Header.Get("Accept-Encoding")))
	return keyBuilder.String()
}
```

</div>

<div>

### Observations

- This function generates a **unique cache key** per request based on:
  - HTTP method, URL scheme, host, path, and query string.
  - Headers like `Accept` and `Accept-Encoding` for content negotiation.
- Ensures that different client variants (e.g. `gzip`, `json`) do **not share** the same cached response.
- The consistent structure enables safe reuse of cached entries **across upstream targets**.


</div>

</div>


---

###  Code Example: Body Hashing

This section is part of the `serveHTTP` function, which handles requests forwarded to the upstream server. Below is the cache-specific logic extracted from the function:

<div class="grid grid-cols-2 gap-6 leading-relaxed">

<div>

```go
// Buffer body for hashing
func (proxy *ReverseProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
var bodyHash string
if req.Body != nil {
  if bodyBytes, err := io.ReadAll(req.Body); err == nil {
    if len(bodyBytes) > 0 {
      sum := sha256.Sum256(bodyBytes)
      bodyHash = hex.EncodeToString(sum[:])
    }
    req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
  }
}

// Append body hash to cache key if available
if bodyHash != "" {
  cacheKey += "|bh=" + bodyHash
}

// Stash key in context for reuse on MISS.
req = req.WithContext(context.WithValue(req.Context(), cacheKeyCtxKey{}, cacheKey))
}
```

</div>

<div>

### Observations


- If body hashing is used, the body hash is appended separately (`|bh=...`).
- To uniquely identify them, we hash the body and append it to the cache key.
- This ensures that even requests to the same URL with **different bodies** produce **distinct cache entries**.
- The body is safely **restored** after reading to avoid breaking the request flow.

</div>

</div>

---

###  Code Example: Cache HIT

This section is part of the `serveHTTP` function, which handles requests forwarded to the upstream server. Below is the cache-specific logic extracted from the function:

<div class="grid grid-cols-2 gap-6 leading-relaxed">

<div>

```go
func (proxy *ReverseProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
if cachedEntry, found, isStale := proxy.cache.Get(cacheKey); found && !isStale {
  copyHeader(w.Header(), cachedEntry.Header)
  w.Header().Set("X-Cache", "HIT")
  ageSeconds := int(time.Since(cachedEntry.StoredAt).Seconds())
  w.Header().Set("Age", strconv.Itoa(max(0, ageSeconds)))
  w.WriteHeader(cachedEntry.StatusCode)
  _, _ = w.Write(cachedEntry.Body)
  return
}
}
```

</div>

<div>

### Observations

- **Retrieves** entry from in-memory cache if found and not stale.
- Sends:
  - `X-Cache: HIT`  
  - `Age` (based on time since cache storage)
- Skips upstream completely → **low latency, high efficiency**.

</div>

</div>
---

###  Code Example: Cache MISS

This section is part of the `serveUpstream` function, which handles requests forwarded to the upstream server. Below is the cache-specific logic extracted from the function:

<div class="grid grid-cols-2 gap-6 leading-relaxed">

<div>

```go
func (proxy *ReverseProxy) serveUpstream(w http.ResponseWriter, req *http.Request) {
  // Response from upstream, potentially cached if eligible
  isRequestEligibleForCache := proxy.cacheOn && isCacheableRequest(outboundReq) && !clientNoCache(outboundReq)
  cacheTTL, isCacheableResponse := isCacheableResponse(respWithBody(statusCode, upstreamResp.Header))
  xCacheState := "BYPASS"
  if isRequestEligibleForCache && isCacheableResponse {
    xCacheState = "MISS"
  }
  // Store in cache if eligible
  if isRequestEligibleForCache && isCacheableResponse {
    proxy.cache.Set(cacheKey, &CachedResponse{
      StatusCode: statusCode,
      Header: sanitizedHeaders,
      Body: responseBody,
      StoredAt: time.Now(),
      RequestID: getRequestID(req),
    }, cacheTTL)
  }
}
```

</div>

<div>

### Observations
- **Sets**:
  - `X-Cache: MISS` if the response is eligible for caching.
  - `X-Cache: BYPASS` if caching is not applicable.
- **Stores**: Saves the upstream response in the cache for future requests if eligible.
- Ensures the client receives the response while preparing the cache for subsequent requests.

</div>

</div>

---

###  How does a request goes to upstream?

<div class="grid grid-cols-2 gap-6">

```mermaid {scale: 0.5, theme: 'neutral'}
graph TB
  A[Proxy Initialization] -->|NewReverseProxy builds instance| B[Request Entry Point]
  B -->|All requests enter through ServeHTTP| C[Short-circuits Before Upstream]
  C -->|Health check or method not allowed| D[Direct Response]
  C -->|Valid request| E[Cache HIT Attempt]
```

```mermaid {scale: 0.5, theme: 'neutral'}
graph TB
  E[Cache HIT Attempt] -->|Cacheable and fresh entry found| F[Serve from Cache]
  E -->|Cache MISS or No Cache| G[Pick Upstream]

  G -->|No upstream available| H[Return 503]
  G -->|Upstream selected| I[Store Upstream in Context]
  I -->|Call serveUpstream| J[Request Reaches Upstream Logic]
```

</div>

---

###  Caching — Tests

<style>
table {
  font-size: 0.70rem;
  line-height: 1.1;
}
</style>

| **Test Category**         | **Description**                                                             | **Function(s)**                                                             |
|----------------------------|-----------------------------------------------------------------------------|-----------------------------------------------------------------------------|
| **Cache HIT/MISS**         | Verifies cache population on a MISS and reuse on subsequent requests.       | `TestCache_HitAndMiss`                                                     |
| **Cache-Control Directives** | Ensures compliance with `no-cache` and `no-store` headers.                 | `TestCache_RespectsNoCacheRequestDirective`                                |
| **TTL Expiry**             | Confirms cache entries expire as per `max-age` and refresh on access.       | `TestCache_ExpiryAndRefetch`                                               |
| **Request Methods**        | Tests caching for various HTTP methods.                                     | `TestCache_POST_Hit`, `TestCache_PUT_Hit`, `TestCache_PATCH_Hit`, `TestCache_DELETE_Hit`, `TestCache_HEAD_Hit` |
| **Body Hashing**           | Validates unique cache entries for requests with different bodies.          | `TestCache_POST_DifferentBodies_NotHit`                                    |
| **Allowed Methods**        | Ensures only permitted HTTP methods interact with the cache.                | `TestDisallowedMethod_NoCacheInteraction`, `TestAllowedMethod_CacheWorksWithRestriction` |

These tests ensure correctness, performance, and adherence to HTTP caching standards.

---
clicks: 4
---

### Caching — Results from Demo Environment

- **Requests**: 8 requests sent to the same endpoint.

<!-- Fixed title block -->
<div class="relative h-8 text-xl font-semibold mt-4">
  <div v-click="[1]" class="absolute inset-0">Proxy Dashboard</div>
  <div v-click="[2]" class="absolute inset-0">Upstream Dashboard</div>
  <div v-click="[3]" class="absolute inset-0">Cache MISS Response Header</div>
  <div v-click="[4]" class="absolute inset-0">Cache HIT Response Header</div>
</div>

<!-- Fixed image block -->
<div class="relative w-full h-[400px] mt-4">
  <img v-click="[1]" src="./photos/cache/01test_proxy_dashboard.png" class="absolute inset-0 w-full h-full object-contain">
  <img v-click="[2]" src="./photos/cache/01test_upsteram_dashboard.png" class="absolute inset-0 w-full h-full object-contain">
  <img v-click="[3]" src="./photos/cache/01test_miss_response_header.png" class="absolute inset-0 w-full h-full object-contain">
  <img v-click="[4]" src="./photos/cache/01test_hit_response_header.png" class="absolute inset-0 w-full h-full object-contain">
</div>

---

## Load Balancer and Health Checker

- Distributes traffic across multiple upstream servers.
- Ensures high availability and scalability.
- Supports two strategies:
  - **Round Robin**: Distributes requests evenly across all servers.
  - **Least Connections**: Routes requests to the server with the fewest active connections.
- Includes **health checks** to detect and exclude unhealthy servers.

---

### Load Balancer Implementation in FCReverseProxy

- **Purpose**: Distribute traffic efficiently and ensure fault tolerance.
- **Strategies**:
  - **Round Robin**: Cycles through servers in order.
  - **Least Connections**: Prefers servers with fewer active requests.
- **Health Checks**:
  - Enabled by default.
  - Excludes unhealthy servers from the pool.

---

### How It Works

<div class="grid grid-cols-2 gap-6 leading-relaxed">

<div>

<div class="mb-6">
  <h3>1. Strategy Selection</h3>
  <ul>
    <li>Configured via <code>ConfigureBalancer</code></li>
    <li>Supports <code>round_robin</code> and <code>least_connections</code></li>
  </ul>
</div>

<div class="mb-6">
  <h3>2. Health Checks</h3>
  <ul>
    <li>Enabled/disabled via <code>SetHealthCheckEnabled</code></li>
    <li>Excludes unhealthy servers from selection</li>
  </ul>
</div>

</div>

<div>

<div class="mb-6">
  <h3>3. Round Robin</h3>
  <ul>
    <li>Distributes requests evenly</li>
    <li>Ignores server load</li>
  </ul>
</div>

<div class="mb-6">
  <h3>4. Least Connections</h3>
  <ul>
    <li>Prefers servers with fewer active requests</li>
    <li>Balances load dynamically</li>
    <li>If multiple servers have the same load, prioritizes the first healthy server(possible improvement)</li>
  </ul>
</div>

</div>

</div>
---

### Code Example: `isTargetHealthy`

<div class="grid grid-cols-2 gap-6 leading-relaxed">

<div>

```go
func isTargetHealthy(targetURL *url.URL) bool {
	// Build absolute health URL at root (/healthz).
	scheme := targetURL.Scheme
	if scheme == "" {
		scheme = "http"
	}
	healthURL := &url.URL{
		Scheme: scheme,
		Host:   targetURL.Host,
		Path:   "/healthz",
	}
	healthRequest, err := http.NewRequest("GET", healthURL.String(), nil)
	if err != nil {
		return false
	}
	// Hint to avoid connection reuse issues on failing endpoints.
	healthRequest.Close = true
	healthResponse, err := healthProbeHTTPClient.Do(healthRequest)
	if err != nil {
		return false
	}
	defer healthResponse.Body.Close()
	// Consider 2xx/3xx as healthy.
	return healthResponse.StatusCode >= 200 && healthResponse.StatusCode < 400
}
```

</div>

<div>

### Observation

- The function performs a **shallow health check** against the `/healthz` endpoint of each upstream target.
- It uses the same scheme and issues a **GET request**.
- Declares targets as healthy **only if they return 2xx or 3xx responses**, ensuring a conservative approach to availability.

</div>

</div>


---

### Code Example Round Robin: `Pick`

<div class="grid grid-cols-2 gap-6 leading-relaxed">
<div>

```go
func (b *roundRobinBalancer) Pick(previewOnly bool) *url.URL {
	if len(b.targets) == 0 {
		return nil
	}
	if previewOnly {
		n := atomic.LoadUint64(&b.nextIndex)
		return b.targets[n%uint64(len(b.targets))]
	}
	startIndex := atomic.AddUint64(&b.nextIndex, 1) - 1
	targetCount := uint64(len(b.targets))

	if !b.healthChecksEnabled {
		return b.targets[startIndex%targetCount]
	}

	for i := uint64(0); i < targetCount; i++ {
		candidateTarget := b.targets[(startIndex+i)%targetCount]
		if isTargetHealthy(candidateTarget) {
			return candidateTarget // Important so if healthy,  does not need to check the others
		}
	}
	return nil
}
```

</div>
<div>

### Observation

- Implements a **simple, fair** rotation through available targets.
- If `healthChecksEnabled`, it checks for healthy targets in order(worst case).
- Uses atomic operations to ensure **thread-safe indexing**.
- Returns `nil` if no targets or no healthy target is found.


</div>
</div>

---

### Code Example Least Connections: `Pick`

<div class="grid grid-cols-2 gap-6 leading-relaxed">
<div>

```go
func (b *leastConnectionsBalancer) Pick(previewOnly bool) *url.URL {
	if len(b.targetStates) == 0 {
		return nil
	}

	findCandidates := func(includePending bool) ([]*lcState, bool) {
		min := int64(math.MaxInt64)
		cands := []*lcState{}
		for _, st := range b.targetStates {
			if b.healthChecksEnabled && !isTargetHealthy(st.upstreamURL) {
				continue
			}
			load := atomic.LoadInt64(&st.activeConnections)
			if includePending {
				load += atomic.LoadInt64(&st.pendingSelections)
			}
			if load < min {
				min = load
				cands = []*lcState{st}
			} else if load == min {
				cands = append(cands, st) 
			}
		}
		return cands, len(cands) > 0
	}

	if previewOnly {
		if cands, ok := findCandidates(false); ok {
			return cands[0].upstreamURL
		}
		return nil
	}

	for {
		cands, ok := findCandidates(true)
		if !ok {
			if !b.healthChecksEnabled {
				for _, st := range b.targetStates {
					return st.upstreamURL
				}
			}
			return nil
		}
		best := cands[0]
		p := atomic.LoadInt64(&best.pendingSelections)
		if atomic.CompareAndSwapInt64(&best.pendingSelections, p, p+1) {
			return best.upstreamURL
		}
	}
}
```

</div>
<div>

### Observation

- Picks the **least loaded** upstream using `activeConnections` and optionally `pendingSelections`.
- Supports health checks and skips unhealthy backends.
- Uses **atomic CAS** to safely reserve pending slots under contention.
- In preview mode, avoids any state mutation.
- Ideal for **high-concurrency systems** where minimizing overload is key.

</div>
</div>

---

### Load Balancer and Health Checker — Tests

<style>
table {
  font-size: 0.70rem;
  line-height: 1.1;
}
</style>

| **Test Category**                         | **Description**                                                                | **Function**                                      |
|--------------------------------------|-----------------------------------------------------------------------------|--------------------------------------------------|
| **Round Robin — Order/Rotation**         | Ensures even, deterministic rotation without health checks.                | `TestRoundRobinBalancer`                         |
| **Least Connections — Basic Selection**  | Picks target with the fewest active connections and reflects acquire/release. | `TestLeastConnectionsBalancerBasic`              |
| **Health Checks — Round Robin**          | Skips unhealthy targets; only healthy backends are selected.               | `TestRoundRobinBalancerHealthChecks`             |
| **Health Checks — LC Tie-break**        | Skips unhealthy; deterministic tie-break among equally loaded healthy targets. | `TestLeastConnectionsBalancerHealthChecks`    |
| **All Backends Unhealthy**              | Returns nil when no healthy targets are available.                          | `TestRoundRobinBalancerHealthAllUnhealthy`       |

These tests validate fair distribution, correct connection accounting, and resilience under backend failures.



---
clicks: 3
---

### Load Balancear — Results from Demo Environment

- **Requests**: 12 requests sent to different endpoints using the **Round Robin (RR)** strategy.

<!-- Fixed title block -->
<div class="relative h-8 text-xl font-semibold mt-4">
  <div v-click="[1]" class="absolute inset-0">Proxy Dashboard </div>
   <div v-click="[2]" class="absolute inset-0">Proxy Dashboard - Total requests by X-Upstream (cumulative)</div>
  <div v-click="[3]" class="absolute inset-0">Upstream Dashboard</div>
</div>

<!-- Fixed image block -->
<div class="relative w-full h-[400px] mt-4">
  <img v-click="[1]" src="./photos/balancear/02_test_totalRequest_rp.png" class="absolute inset-0 w-full h-full object-contain">
  <img v-click="[2]" src="./photos/balancear/02_test_byUpstream.png" class="absolute inset-0 w-full h-full object-contain">
  <img v-click="[3]" src="./photos/balancear/02_test_totalRequest_upstream.png" class="absolute inset-0 w-full h-full object-contain">
</div>

---

---

# Themes

Slidev comes with powerful theming support. Themes can provide styles, layouts, components, or even configurations for tools. Switching between themes by just **one edit** in your frontmatter:

<div grid="~ cols-2 gap-2" m="t-2">

```yaml
---
theme: default
---
```

```yaml
---
theme: seriph
---
```

<img border="rounded" src="https://github.com/slidevjs/themes/blob/main/screenshots/theme-default/01.png?raw=true" alt="">

<img border="rounded" src="https://github.com/slidevjs/themes/blob/main/screenshots/theme-seriph/01.png?raw=true" alt="">

</div>

Read more about [How to use a theme](https://sli.dev/guide/theme-addon#use-theme) and
check out the [Awesome Themes Gallery](https://sli.dev/resources/theme-gallery).

---

# Clicks Animations

You can add `v-click` to elements to add a click animation.

<div v-click>

This shows up when you click the slide:

```html
<div v-click>This shows up when you click the slide.</div>
```

</div>

<br>

<v-click>

The <span v-mark.red="3"><code>v-mark</code> directive</span>
also allows you to add
<span v-mark.circle.orange="4">inline marks</span>
, powered by [Rough Notation](https://roughnotation.com/):

```html
<span v-mark.underline.orange>inline markers</span>
```

</v-click>

<div mt-20 v-click>

[Learn more](https://sli.dev/guide/animations#click-animation)

</div>

---

# Motions

Motion animations are powered by [@vueuse/motion](https://motion.vueuse.org/), triggered by `v-motion` directive.

```html
<div
  v-motion
  :initial="{ x: -80 }"
  :enter="{ x: 0 }"
  :click-3="{ x: 80 }"
  :leave="{ x: 1000 }"
>
  Slidev
</div>
```

<div class="w-60 relative">
  <div class="relative w-40 h-40">
    <img
      v-motion
      :initial="{ x: 800, y: -100, scale: 1.5, rotate: -50 }"
      :enter="final"
      class="absolute inset-0"
      src="https://sli.dev/logo-square.png"
      alt=""
    />
    <img
      v-motion
      :initial="{ y: 500, x: -100, scale: 2 }"
      :enter="final"
      class="absolute inset-0"
      src="https://sli.dev/logo-circle.png"
      alt=""
    />
    <img
      v-motion
      :initial="{ x: 600, y: 400, scale: 2, rotate: 100 }"
      :enter="final"
      class="absolute inset-0"
      src="https://sli.dev/logo-triangle.png"
      alt=""
    />
  </div>

  <div
    class="text-5xl absolute top-14 left-40 text-[#2B90B6] -z-1"
    v-motion
    :initial="{ x: -80, opacity: 0}"
    :enter="{ x: 0, opacity: 1, transition: { delay: 2000, duration: 1000 } }">
    Slidev
  </div>
</div>

<!-- vue script setup scripts can be directly used in markdown, and will only affects current page -->
<script setup lang="ts">
const final = {
  x: 0,
  y: 0,
  rotate: 0,
  scale: 1,
  transition: {
    type: 'spring',
    damping: 10,
    stiffness: 20,
    mass: 2
  }
}
</script>

<div
  v-motion
  :initial="{ x:35, y: 30, opacity: 0}"
  :enter="{ y: 0, opacity: 1, transition: { delay: 3500 } }">

[Learn more](https://sli.dev/guide/animations.html#motion)

</div>

---

# LaTeX

LaTeX is supported out-of-box. Powered by [KaTeX](https://katex.org/).

<div h-3 />

Inline $\sqrt{3x-1}+(1+x)^2$

Block
$$ {1|3|all}
\begin{aligned}
\nabla \cdot \vec{E} &= \frac{\rho}{\varepsilon_0} \\
\nabla \cdot \vec{B} &= 0 \\
\nabla \times \vec{E} &= -\frac{\partial\vec{B}}{\partial t} \\
\nabla \times \vec{B} &= \mu_0\vec{J} + \mu_0\varepsilon_0\frac{\partial\vec{E}}{\partial t}
\end{aligned}
$$

[Learn more](https://sli.dev/features/latex)

---

# Diagrams

You can create diagrams / graphs from textual descriptions, directly in your Markdown.

<div class="grid grid-cols-4 gap-5 pt-4 -mb-6">

```mermaid {scale: 0.5, alt: 'A simple sequence diagram'}
sequenceDiagram
    Alice->John: Hello John, how are you?
    Note over Alice,John: A typical interaction
```

```mermaid {theme: 'neutral', scale: 0.8}
graph TD
B[Text] --> C{Decision}
C -->|One| D[Result 1]
C -->|Two| E[Result 2]
```

```mermaid
mindmap
  root((mindmap))
    Origins
      Long history
      ::icon(fa fa-book)
      Popularisation
        British popular psychology author Tony Buzan
    Research
      On effectiveness<br/>and features
      On Automatic creation
        Uses
            Creative techniques
            Strategic planning
            Argument mapping
    Tools
      Pen and paper
      Mermaid
```

```plantuml {scale: 0.7}
@startuml

package "Some Group" {
  HTTP - [First Component]
  [Another Component]
}

node "Other Groups" {
  FTP - [Second Component]
  [First Component] --> FTP
}

cloud {
  [Example 1]
}

database "MySql" {
  folder "This is my folder" {
    [Folder 3]
  }
  frame "Foo" {
    [Frame 4]
  }
}

[Another Component] --> [Example 1]
[Example 1] --> [Folder 3]
[Folder 3] --> [Frame 4]

@enduml
```

</div>

Learn more: [Mermaid Diagrams](https://sli.dev/features/mermaid) and [PlantUML Diagrams](https://sli.dev/features/plantuml)

---
foo: bar
dragPos:
  square: 691,32,167,_,-16
---

# Draggable Elements

Double-click on the draggable elements to edit their positions.

<br>

###### Directive Usage

```md
<img v-drag="'square'" src="https://sli.dev/logo.png">
```

<br>

###### Component Usage

```md
<v-drag text-3xl>
  <div class="i-carbon:arrow-up" />
  Use the `v-drag` component to have a draggable container!
</v-drag>
```

<v-drag pos="663,206,261,_,-15">
  <div text-center text-3xl border border-main rounded>
    Double-click me!
  </div>
</v-drag>

<img v-drag="'square'" src="https://sli.dev/logo.png">

###### Draggable Arrow

```md
<v-drag-arrow two-way />
```

<v-drag-arrow pos="67,452,253,46" two-way op70 />

---
src: ./pages/imported-slides.md
hide: false
---

---

# Monaco Editor

Slidev provides built-in Monaco Editor support.

Add `{monaco}` to the code block to turn it into an editor:

```ts {monaco}
import { ref } from 'vue'
import { emptyArray } from './external'

const arr = ref(emptyArray(10))
```

Use `{monaco-run}` to create an editor that can execute the code directly in the slide:

```ts {monaco-run}
import { version } from 'vue'
import { emptyArray, sayHello } from './external'

sayHello()
console.log(`vue ${version}`)
console.log(emptyArray<number>(10).reduce(fib => [...fib, fib.at(-1)! + fib.at(-2)!], [1, 1]))
```

---
layout: center
class: text-center
---

# Learn More

[Documentation](https://sli.dev) · [GitHub](https://github.com/slidevjs/slidev) · [Showcases](https://sli.dev/resources/showcases)

<PoweredBySlidev mt-10 />
