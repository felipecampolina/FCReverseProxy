package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	applog "traefik-challenge-2/internal/log"
	imetrics "traefik-challenge-2/internal/metrics"
)

type ReverseProxy struct {
	// Upstream destination used when a single backend is configured.
	target *url.URL
	// All upstream destinations (used by the balancer).
	targets []*url.URL
	// HTTP transport used to communicate with upstreams.
	transport *http.Transport
	// Cache implementation (interface) used to store cacheable responses.
	cache Cache
	// Global toggle to enable/disable the caching layer.
	cacheOn bool
	// Handler used for the upstream path; may be wrapped (e.g., by a queue).
	handler http.Handler
	// Optional request method allowlist; nil means allow all.
	allowedMethods map[string]struct{}
	// Load balancer strategy/instance used to pick/track upstreams.
	balancer Balancer
	lbStrategy string
	// Whether active health checks are enabled in the balancer.
	healthChecksEnabled bool
}

// Creates a new ReverseProxy instance with the specified target, cache, and cache toggle.
// The default balancer is round-robin ("rr") and health checks are enabled.
func NewReverseProxy(target *url.URL, cache Cache, cacheOn bool) *ReverseProxy {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	proxyInstance := &ReverseProxy{
		target:    target,
		targets:   []*url.URL{target},
		transport: transport,
		cache:     cache,
		cacheOn:   cacheOn,
		// defaults
		lbStrategy:          "rr",
		healthChecksEnabled: true,
	}
	// Default handler (queued wrapper may be added later); upstream only.
	proxyInstance.handler = http.HandlerFunc(proxyInstance.serveUpstream)
	proxyInstance.balancer = newBalancer(proxyInstance.lbStrategy, proxyInstance.targets, proxyInstance.healthChecksEnabled)
	return proxyInstance
}

// NewReverseProxyMulti builds a reverse proxy over multiple upstream targets (round-robin).
func NewReverseProxyMulti(targets []*url.URL, cache Cache, cacheOn bool) *ReverseProxy {
	if len(targets) == 0 {
		panic("NewReverseProxyMulti requires at least one target")
	}
	proxyInstance := NewReverseProxy(targets[0], cache, cacheOn)
	proxyInstance.targets = append([]*url.URL{}, targets...)
	proxyInstance.balancer = newBalancer(proxyInstance.lbStrategy, proxyInstance.targets, proxyInstance.healthChecksEnabled)
	return proxyInstance
}

// Enable bounded queue + concurrency cap by wrapping with queue.WithQueue (only used on upstream path).
func (proxy *ReverseProxy) WithQueue(cfg QueueConfig) *ReverseProxy {
	proxy.handler = WithQueue(http.HandlerFunc(proxy.serveUpstream), cfg)
	return proxy
}

// SetAllowedMethods configures which HTTP methods are permitted (empty slice => allow all).
func (proxy *ReverseProxy) SetAllowedMethods(methods []string) {
	if len(methods) == 0 {
		proxy.allowedMethods = nil
		return
	}
	allowed := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		allowed[strings.ToUpper(strings.TrimSpace(method))] = struct{}{}
	}
	proxy.allowedMethods = allowed
}

// listAllowedMethods returns a sorted slice (used for Allow header).
func (proxy *ReverseProxy) listAllowedMethods() []string {
	if proxy.allowedMethods == nil {
		return nil
	}
	methods := make([]string, 0, len(proxy.allowedMethods))
	for method := range proxy.allowedMethods {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return methods
}

// Handles incoming HTTP requests and routes them to the appropriate target.
// Flow:
//   - Special-case /healthz
//   - Enforce allowed methods (405)
//   - Optionally compute a cache key and try to serve a HIT
//   - Select upstream; if none healthy -> 503
//   - Forward upstream (queued handler may wrap); observe/log/optionally cache
func (proxy *ReverseProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Record the start time for end-to-end latency metrics and logging.
	startTime := time.Now()
	req = req.WithContext(context.WithValue(req.Context(), startTimeCtxKey{}, startTime))

	// Health check endpoint (bypass queue, cache, and upstream).
	if req.URL.Path == "/healthz" {
		if requestID := getRequestID(req); requestID != "" {
			w.Header().Set("X-Request-ID", requestID)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	// Enforce allowed methods (after health check).
	if proxy.allowedMethods != nil {
		if _, ok := proxy.allowedMethods[req.Method]; !ok {
			if allow := proxy.listAllowedMethods(); len(allow) > 0 {
				w.Header().Set("Allow", strings.Join(allow, ", "))
			}
			if requestID := getRequestID(req); requestID != "" {
				w.Header().Set("X-Request-ID", requestID)
			}
			imetrics.ObserveProxyResponse(req.Method, http.StatusMethodNotAllowed, "BYPASS", time.Since(startTime))
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}

	// Pre-select a target to build upstream-shaped cache keys consistently.
	selectedTarget := proxy.balancer.Pick(true)

	if proxy.cacheOn && req != nil {
		// Read & buffer body (if any) so it can be hashed and reused downstream.
		var bodyHash string
		if req.Body != nil {
			if bodyBytes, err := io.ReadAll(req.Body); err == nil {
				if len(bodyBytes) > 0 {
					sum := sha256.Sum256(bodyBytes)
					bodyHash = hex.EncodeToString(sum[:])
				}
				// Restore body for further handling
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
		}

		// Clone for cache key calculation and upstream URL rewriting.
		cacheProbeReq := req.Clone(req.Context())
		if selectedTarget != nil {
			proxy.directRequest(cacheProbeReq, selectedTarget)
		}

		if isCacheableRequest(cacheProbeReq) && !clientNoCache(cacheProbeReq) {
			// Build cache key based on client-facing URL/host so different upstreams share cache objects.
			originalClientHost := req.Host
			upstreamReqHost := cacheProbeReq.Host
			upstreamURLHost := cacheProbeReq.URL.Host
			cacheProbeReq.Host = originalClientHost
			cacheProbeReq.URL.Host = originalClientHost
			cacheKey := buildCacheKey(cacheProbeReq)
			// Restore upstream host fields for any later use.
			cacheProbeReq.Host = upstreamReqHost
			cacheProbeReq.URL.Host = upstreamURLHost

			if bodyHash != "" {
				cacheKey += "|bh=" + bodyHash
			}
			// Stash key in context for reuse on MISS.
			req = req.WithContext(context.WithValue(req.Context(), cacheKeyCtxKey{}, cacheKey))

			// Attempt a cache HIT.
			if cachedEntry, found, isStale := proxy.cache.Get(cacheKey); found && !isStale {
				// Prefer the original request ID that produced this cache entry.
				requestID := strings.TrimSpace(cachedEntry.RequestID)
				if requestID == "" {
					requestID = ensureRequestID(req)
				} else {
					req.Header.Set("X-Request-ID", requestID)
				}
				w.Header().Set("X-Request-ID", requestID)

				// Log cache hit
				applog.LogProxyRequestCacheHit(req)

				// Write cached response
				copyHeader(w.Header(), cachedEntry.Header)
				w.Header().Set("X-Cache", "HIT")
				ageSeconds := int(time.Since(cachedEntry.StoredAt).Seconds())
				if ageSeconds < 0 {
					ageSeconds = 0
				}
				w.Header().Set("Age", strconv.Itoa(ageSeconds))

				w.WriteHeader(cachedEntry.StatusCode)
				_, _ = w.Write(cachedEntry.Body)

				// Observe HIT metrics
				imetrics.ObserveProxyResponse(req.Method, cachedEntry.StatusCode, "HIT", time.Since(startTime))

				// Log response
				applog.LogProxyResponseCacheHit(
					cachedEntry.StatusCode,
					len(cachedEntry.Body),
					time.Since(startTime),
					w.Header(),
					req,
					w,
					false,
					"",
				)
				return
			}
		}
	}

	// No HIT, advance balancer state to choose actual upstream.
	selectedTarget = proxy.balancer.Pick(false)
	if selectedTarget == nil {
		// No healthy upstreams.
		if requestID := getRequestID(req); requestID != "" {
			w.Header().Set("X-Request-ID", requestID)
		}
		imetrics.ObserveProxyResponse(req.Method, http.StatusServiceUnavailable, "BYPASS", time.Since(startTime))
		applog.LogProxyError(http.StatusServiceUnavailable, "BYPASS", "", req, fmt.Errorf("no healthy upstream targets"))
		http.Error(w, "no healthy upstream targets", http.StatusServiceUnavailable)
		return
	}

	// We are going upstream: ensure we have a request ID and echo it.
	requestID := ensureRequestID(req)
	w.Header().Set("X-Request-ID", requestID)

	// MISS/BYPASS request log before forwarding upstream.
	applog.LogProxyRequest(req)

	// Store chosen target for reuse by upstream path (and potential queue wrapper).
	req = req.WithContext(context.WithValue(req.Context(), upstreamTargetCtxKey{}, selectedTarget))
	proxy.handler.ServeHTTP(w, req)
}

// Core upstream path (no cache-hit logic; queue may wrap this).
// Responsible for: rewriting request, forwarding, collecting metrics, and optionally caching response.
func (proxy *ReverseProxy) serveUpstream(w http.ResponseWriter, req *http.Request) {
	upstreamStartTime := time.Now()
	ctx := req.Context()

	// Prefer ServeHTTP start time for end-to-end metrics, fallback to local start.
	endToEndStart, _ := ctx.Value(startTimeCtxKey{}).(time.Time)
	if endToEndStart.IsZero() {
		endToEndStart = upstreamStartTime
	}

	// Reuse previously chosen target (from cache phase) if present; otherwise pick now.
	var upstreamTarget *url.URL
	if v := ctx.Value(upstreamTargetCtxKey{}); v != nil {
		if u, ok := v.(*url.URL); ok && u != nil {
			upstreamTarget = u
		}
	}
	if upstreamTarget == nil {
		upstreamTarget = proxy.balancer.Pick(false)
	}
	if upstreamTarget == nil {
		imetrics.ObserveProxyResponse(req.Method, http.StatusServiceUnavailable, "BYPASS", time.Since(endToEndStart))
		http.Error(w, "no healthy upstream targets", http.StatusServiceUnavailable)
		return
	}

	// Acquire increments active in-flight counters for the selected upstream.
	releaseFunc := proxy.balancer.Acquire(upstreamTarget)
	defer releaseFunc()

	// Clone and rewrite the outbound request for the selected upstream.
	outboundReq := req.Clone(ctx)
	proxy.directRequest(outboundReq, upstreamTarget)

	// In-flight upstream metric (per target).
	imetrics.IncProxyUpstreamInflight(upstreamTarget.Host)
	defer imetrics.DecProxyUpstreamInflight(upstreamTarget.Host)

	// Forward request to upstream
	upstreamResp, err := proxy.transport.RoundTrip(outboundReq)
	if err != nil {
		statusCode := http.StatusBadGateway
		if ctx.Err() != nil {
			statusCode = http.StatusRequestTimeout
		}
		imetrics.ObserveProxyUpstreamResponse(upstreamTarget.Host, req.Method, statusCode, time.Since(upstreamStartTime))
		// Also observe final proxy response (bypass cache)
		imetrics.ObserveProxyResponse(req.Method, statusCode, "BYPASS", time.Since(endToEndStart))

		applog.LogProxyError(statusCode, "BYPASS", upstreamTarget.Host, req, err)

		select {
		case <-ctx.Done():
			w.WriteHeader(http.StatusRequestTimeout)
		default:
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
		return
	}
	defer upstreamResp.Body.Close()

	// Read upstream response entirely (buffer for potential caching).
	responseBody, readErr := io.ReadAll(upstreamResp.Body)
	if readErr != nil {
		http.Error(w, readErr.Error(), http.StatusBadGateway)
		return
	}

	// Use raw upstream headers for cacheability/TTL decisions,
	rawUpstreamHeaders := upstreamResp.Header.Clone()
	sanitizedHeaders := sanitizeResponseHeaders(rawUpstreamHeaders)
	statusCode := upstreamResp.StatusCode

	// Determine X-Cache header value
	isRequestEligibleForCache := proxy.cacheOn && isCacheableRequest(outboundReq) && !clientNoCache(outboundReq)
	cacheTTL, isCacheableResponse := isCacheableResponse(respWithBody(statusCode, rawUpstreamHeaders))
	xCacheState := "BYPASS"
	if isRequestEligibleForCache && isCacheableResponse {
		xCacheState = "MISS"
	}

	// Write headers and body to the client
	copyHeader(w.Header(), sanitizedHeaders)
	if _, ok := w.Header()["Content-Length"]; !ok {
		w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
	}
	w.Header().Set("X-Cache", xCacheState)
	w.WriteHeader(statusCode)
	_, _ = w.Write(responseBody)

	// Per-upstream observation
	upstreamLabel := rawUpstreamHeaders.Get("X-Upstream")
	if strings.TrimSpace(upstreamLabel) == "" {
		upstreamLabel = upstreamTarget.Host
	}
	upstreamDuration := time.Since(upstreamStartTime)
	imetrics.ObserveProxyUpstreamResponse(upstreamLabel, req.Method, statusCode, upstreamDuration)

	// End-to-end proxy response (MISS or BYPASS)
	imetrics.ObserveProxyResponse(req.Method, statusCode, xCacheState, time.Since(endToEndStart))

	// Log response
	applog.LogProxyResponseCacheHit(
		statusCode,
		len(responseBody),
		upstreamDuration,
		w.Header(),
		req,
		w,
		false,
		"",
	)

	// Cache the response if eligible (on MISS)
	if isRequestEligibleForCache && isCacheableResponse {
		// Reuse precomputed key (with body hash) if available
		cacheKey, _ := req.Context().Value(cacheKeyCtxKey{}).(string)
		if cacheKey == "" {
			// Fallback (no body hash) — should rarely happen
			cacheKey = buildCacheKey(outboundReq)
		}
		proxy.cache.Set(cacheKey, &CachedResponse{
			StatusCode: statusCode,
			Header:     sanitizedHeaders,
			Body:       responseBody,
			StoredAt:   time.Now(),
			RequestID:  getRequestID(req),
		}, cacheTTL)
	}
}

// Rewrites the request URL, path, and hop-by-hop headers before sending to the upstream.
func (proxy *ReverseProxy) directRequest(outReq *http.Request, upstreamTarget *url.URL) {
	// Rewrite URL & path
	outReq.URL.Scheme = upstreamTarget.Scheme
	outReq.URL.Host = upstreamTarget.Host
	outReq.URL.Path = singleJoiningSlash(upstreamTarget.Path, outReq.URL.Path)

	// Remove hop-by-hop headers (per RFC 7230)
	for _, hopHeader := range hopHeaders {
		outReq.Header.Del(hopHeader)
	}

	// Set X-Forwarded-* headers and Host
	if clientIP, _, err := net.SplitHostPort(outReq.RemoteAddr); err == nil && clientIP != "" {
		xff := outReq.Header.Get("X-Forwarded-For")
		if xff == "" {
			outReq.Header.Set("X-Forwarded-For", clientIP)
		} else {
			outReq.Header.Set("X-Forwarded-For", xff+", "+clientIP)
		}
	}
	outReq.Header.Set("X-Forwarded-Proto", schemeOf(outReq))
	outReq.Header.Set("X-Forwarded-Host", outReq.Host)
	outReq.Host = upstreamTarget.Host
}

// ConfigureBalancer switches balancing strategy at runtime.
func (proxy *ReverseProxy) ConfigureBalancer(strategy string) {
	proxy.lbStrategy = strategy
	proxy.balancer = newBalancer(proxy.lbStrategy, proxy.targets, proxy.healthChecksEnabled)
}

// Toggle active health checks in the load balancer at runtime.
func (proxy *ReverseProxy) SetHealthCheckEnabled(enabled bool) {
	proxy.healthChecksEnabled = enabled
	proxy.balancer = newBalancer(proxy.lbStrategy, proxy.targets, proxy.healthChecksEnabled)
}

// context key for cached request key
type cacheKeyCtxKey struct{}
type upstreamTargetCtxKey struct{}
// add context key for request start time (end-to-end measurement)
type startTimeCtxKey struct{}

// Checks if the client explicitly requested no-cache.
func clientNoCache(req *http.Request) bool {
	directives := parseCacheControl(req.Header.Get("Cache-Control"))
	if _, ok := directives["no-cache"]; ok {
		return true
	}
	if _, ok := directives["no-store"]; ok {
		return true
	}
	if strings.EqualFold(req.Header.Get("Pragma"), "no-cache") {
		return true
	}
	return false
}

// Joins two paths with a single slash.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}

// Adds back missing helper used by directRequest.
func schemeOf(req *http.Request) string {
	if req.TLS != nil {
		return "https"
	}
	if sch := req.Header.Get("X-Forwarded-Proto"); sch != "" {
		return sch
	}
	return "http"
}

// Copies headers from the source to the destination.
func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// sanitizeResponseHeaders returns a copy of headers without hop-by-hop headers.
func sanitizeResponseHeaders(headers http.Header) http.Header {
	sanitized := make(http.Header, len(headers))
	for k, vv := range headers {
		for _, v := range vv {
			sanitized.Add(k, v)
		}
	}
	for _, h := range hopHeaders {
		sanitized.Del(h)
	}
	return sanitized
}

// Wraps a response with its status and headers.
func respWithBody(status int, header http.Header) *http.Response {
	return &http.Response{StatusCode: status, Header: header}
}

// Add an atomic counter to help build unique request IDs.
var requestCounter int64

// ensureRequestID sets X-Request-ID on the request if missing and returns it.
func ensureRequestID(req *http.Request) string {
	requestID := strings.TrimSpace(req.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&requestCounter, 1))
		req.Header.Set("X-Request-ID", requestID)
	}
	return requestID
}

// getRequestID returns an existing X-Request-ID without generating a new one.
func getRequestID(req *http.Request) string {
	return strings.TrimSpace(req.Header.Get("X-Request-ID"))
}



