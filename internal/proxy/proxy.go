package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	imetrics "traefik-challenge-2/internal/metrics"
)

type ReverseProxy struct {
	target         *url.URL
	targets        []*url.URL
	transport      *http.Transport
	cache          Cache
	cacheOn        bool
	handler        http.Handler
	allowedMethods map[string]struct{}
	balancer       Balancer
	lbStrategy     string
	healthChecksEnabled bool
}

// Creates a new ReverseProxy instance with the specified target, cache, and cache toggle.
func NewReverseProxy(target *url.URL, cache Cache, cacheOn bool) *ReverseProxy {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	p := &ReverseProxy{
		target:    target,
		targets:   []*url.URL{target},
		transport: tr,
		cache:     cache,
		cacheOn:   cacheOn,
		// defaults
		lbStrategy:          "rr",
		healthChecksEnabled: true,
	}
	// default handler (queued wrapper may be added later); upstream only.
	p.handler = http.HandlerFunc(p.serveUpstream)
	p.balancer = newBalancer(p.lbStrategy, p.targets, p.healthChecksEnabled)
	return p
}

// NewReverseProxyMulti builds a reverse proxy over multiple upstream targets (round-robin).
func NewReverseProxyMulti(targets []*url.URL, cache Cache, cacheOn bool) *ReverseProxy {
	if len(targets) == 0 {
		panic("NewReverseProxyMulti requires at least one target")
	}
	p := NewReverseProxy(targets[0], cache, cacheOn)
	p.targets = append([]*url.URL{}, targets...)
	p.balancer = newBalancer(p.lbStrategy, p.targets, p.healthChecksEnabled)
	return p
}

// Enable bounded queue + concurrency cap by wrapping with queue.WithQueue (only used on upstream path).
func (p *ReverseProxy) WithQueue(cfg QueueConfig) *ReverseProxy {
	p.handler = WithQueue(http.HandlerFunc(p.serveUpstream), cfg)
	return p
}

// SetAllowedMethods configures which HTTP methods are permitted (empty slice => allow all).
func (p *ReverseProxy) SetAllowedMethods(methods []string) {
	if len(methods) == 0 {
		p.allowedMethods = nil
		return
	}
	m := make(map[string]struct{}, len(methods))
	for _, meth := range methods {
		m[strings.ToUpper(strings.TrimSpace(meth))] = struct{}{}
	}
	p.allowedMethods = m
}

// listAllowedMethods returns a sorted slice (used for Allow header).
func (p *ReverseProxy) listAllowedMethods() []string {
	if p.allowedMethods == nil {
		return nil
	}
	out := make([]string, 0, len(p.allowedMethods))
	for k := range p.allowedMethods {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Handles incoming HTTP requests and routes them to the appropriate target.
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// record start and propagate to downstream handlers (includes queue wait)
	start := time.Now()
	r = r.WithContext(context.WithValue(r.Context(), startTimeCtxKey{}, start))

	// Health check endpoint (bypass queue)
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	// Enforce allowed methods (after health check).
	if p.allowedMethods != nil {
		if _, ok := p.allowedMethods[r.Method]; !ok {
			if allow := p.listAllowedMethods(); len(allow) > 0 {
				w.Header().Set("Allow", strings.Join(allow, ", "))
			}
			// observe 405 response at the proxy (bypass cache)
			imetrics.ObserveProxyResponse(r.Method, http.StatusMethodNotAllowed, "BYPASS", time.Since(start))
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}

	// Pre-select target for cache key 
	chosen := p.balancer.Pick(true)

	if p.cacheOn && r != nil {
		// Read & buffer body (if any) so it can be hashed and reused downstream.
		var bodyHash string
		if r.Body != nil {
			if b, err := io.ReadAll(r.Body); err == nil {
				if len(b) > 0 {
					sum := sha256.Sum256(b)
					bodyHash = hex.EncodeToString(sum[:])
				}
				// restore body for further handling
				r.Body = io.NopCloser(bytes.NewReader(b))
			}
		}

		outreq := r.Clone(r.Context())
		// Only rewrite if we have a candidate (avoid nil deref if no targets configured).
		if chosen != nil {
			p.directRequest(outreq, chosen)
		}

		if isCacheableRequest(outreq) && !clientNoCache(outreq) {
			// Build cache key using original client host (not the selected upstream),
			// so different backend choices still hit the same cached object.
			clientHost := r.Host
			upHost := outreq.Host
			upURLHost := outreq.URL.Host
			outreq.Host = clientHost
			outreq.URL.Host = clientHost
			key := buildCacheKey(outreq)
			// restore upstream host fields for any later use
			outreq.Host = upHost
			outreq.URL.Host = upURLHost

			if bodyHash != "" {
				key += "|bh=" + bodyHash
			}
			// stash key in context for reuse on MISS
			r = r.WithContext(context.WithValue(r.Context(), cacheKeyCtxKey{}, key))

			if cached, ok, stale := p.cache.Get(key); ok && !stale {
				// Log cache hit
				logRequestCacheHit(r)

				// Write cached response
				copyHeader(w.Header(), cached.Header)
				w.Header().Set("X-Cache", "HIT")
				// Add/override Age based on when the object was stored
				age := int(time.Since(cached.StoredAt).Seconds())
				if age < 0 {
					age = 0
				}
				w.Header().Set("Age", strconv.Itoa(age))

				w.WriteHeader(cached.StatusCode)
				_, _ = w.Write(cached.Body)

				// observe proxy HIT
				imetrics.ObserveProxyResponse(r.Method, cached.StatusCode, "HIT", time.Since(start))

				// Log response
				logResponseCacheHit(
					cached.StatusCode,
					len(cached.Body),
					time.Since(start),
					w.Header(),
					r,
					w,
					false,
					"",
				)
				return
			}
		}
	}

	// Ensure we advance balancer for real upstream work.
	chosen = p.balancer.Pick(false)
	if chosen == nil {
		// No healthy upstreams: observe and stop the request.
		imetrics.ObserveProxyResponse(r.Method, http.StatusServiceUnavailable, "BYPASS", time.Since(start))
		http.Error(w, "no healthy upstream targets", http.StatusServiceUnavailable)
		return
	}

	// MISS/BYPASS: ensure chosen target stored for reuse & acquisition in upstream path.
	r = r.WithContext(context.WithValue(r.Context(), upstreamTargetCtxKey{}, chosen))
	p.handler.ServeHTTP(w, r)
}

// Core upstream path (no cache-hit logic; queue may wrap this).
func (p *ReverseProxy) serveUpstream(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()

	// prefer ServeHTTP start time for end-to-end metric, fallback to local start
	reqStart, _ := ctx.Value(startTimeCtxKey{}).(time.Time)
	if reqStart.IsZero() {
		reqStart = start
	}

	// Reuse previously chosen target (from cache phase) if present; otherwise pick now.
	var tgt *url.URL
	if v := ctx.Value(upstreamTargetCtxKey{}); v != nil {
		if u, ok := v.(*url.URL); ok && u != nil {
			tgt = u
		}
	}
	if tgt == nil {
		tgt = p.balancer.Pick(false)
	}
	if tgt == nil {
		// observe no healthy upstreams here too (defensive)
		imetrics.ObserveProxyResponse(r.Method, http.StatusServiceUnavailable, "BYPASS", time.Since(reqStart))
		http.Error(w, "no healthy upstream targets", http.StatusServiceUnavailable)
		return
	}

	// Acquire (increments active only for real upstream request).
	release := p.balancer.Acquire(tgt)
	defer release()

	outreq := r.Clone(ctx)
	p.directRequest(outreq, tgt)

	// In-flight upstream metric
	imetrics.IncProxyUpstreamInflight(tgt.Host)
	defer imetrics.DecProxyUpstreamInflight(tgt.Host)

	// Forward request to upstream
	resp, err := p.transport.RoundTrip(outreq)
	if err != nil {
		status := http.StatusBadGateway
		if ctx.Err() != nil {
			status = http.StatusRequestTimeout
		}
		imetrics.ObserveProxyUpstreamResponse(tgt.Host, r.Method, status, time.Since(start))
		// also observe final proxy response (bypass cache)
		imetrics.ObserveProxyResponse(r.Method, status, "BYPASS", time.Since(reqStart))
		select {
		case <-ctx.Done():
			w.WriteHeader(http.StatusRequestTimeout)
		default:
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()

	// Copy response to client and optionally cache it
	buf, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		http.Error(w, readErr.Error(), http.StatusBadGateway)
		return
	}
	// Use raw upstream headers for cacheability/TTL decisions,
	// but sanitize (remove hop-by-hop) for forwarding/storing.
	rawHeaders := resp.Header.Clone()
	cleanHeaders := sanitizeResponseHeaders(rawHeaders)
	status := resp.StatusCode

	// Determine X-Cache header value
	eligibleReq := p.cacheOn && isCacheableRequest(outreq) && !clientNoCache(outreq)
	ttl, cacheableResp := isCacheableResponse(respWithBody(status, rawHeaders))
	xcache := "BYPASS"
	if eligibleReq && cacheableResp {
		xcache = "MISS"
	}

	// Write headers and body to the client
	copyHeader(w.Header(), cleanHeaders)
	// Ensure Content-Length reflects buffered body size if not already set
	if _, ok := w.Header()["Content-Length"]; !ok {
		w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
	}
	w.Header().Set("X-Cache", xcache)
	w.WriteHeader(status)
	_, _ = w.Write(buf)

	// Compute duration once
	dur := time.Since(start)

	upLabel := rawHeaders.Get("X-Upstream")
	if strings.TrimSpace(upLabel) == "" {
		upLabel = tgt.Host
	}
	imetrics.ObserveProxyUpstreamResponse(upLabel, r.Method, status, dur)

	// observe end-to-end proxy response (MISS or BYPASS)
	imetrics.ObserveProxyResponse(r.Method, status, xcache, time.Since(reqStart))

	// Log response
	logResponseCacheHit(
		status,
		len(buf),
		dur,
		w.Header(),
		r,
		w,
		false, // Not applicable for this case
		"",
	)

	// Cache the response if eligible (on MISS)
	if eligibleReq && cacheableResp {
		// Reuse precomputed key (with body hash) if available
		key, _ := r.Context().Value(cacheKeyCtxKey{}).(string)
		if key == "" {
			// fallback (no body hash) — should rarely happen
			key = buildCacheKey(outreq)
		}
		p.cache.Set(key, &CachedResponse{
			StatusCode: status,
			Header:     cleanHeaders,
			Body:       buf,
			StoredAt:   time.Now(),
		}, ttl)
	}
}

// Rewrites the request URL, path, and hop-by-hop headers.
func (p *ReverseProxy) directRequest(outreq *http.Request, tgt *url.URL) {
	// Rewrite URL
	outreq.URL.Scheme = tgt.Scheme
	outreq.URL.Host = tgt.Host
	outreq.URL.Path = singleJoiningSlash(tgt.Path, outreq.URL.Path)

	// Remove hop-by-hop headers
	for _, h := range hopHeaders {
		outreq.Header.Del(h)
	}

	// Set X-Forwarded-* headers and Host
	if clientIP, _, err := net.SplitHostPort(outreq.RemoteAddr); err == nil && clientIP != "" {
		xf := outreq.Header.Get("X-Forwarded-For")
		if xf == "" {
			outreq.Header.Set("X-Forwarded-For", clientIP)
		} else {
			outreq.Header.Set("X-Forwarded-For", xf+", "+clientIP)
		}
	}
	outreq.Header.Set("X-Forwarded-Proto", schemeOf(outreq))
	outreq.Header.Set("X-Forwarded-Host", outreq.Host)
	outreq.Host = tgt.Host
}

// ConfigureBalancer switches balancing strategy at runtime.
func (p *ReverseProxy) ConfigureBalancer(strategy string) {
	p.lbStrategy = strategy
	p.balancer = newBalancer(p.lbStrategy, p.targets, p.healthChecksEnabled)
}

// Toggle active health checks in the load balancer at runtime.
func (p *ReverseProxy) SetHealthCheckEnabled(enabled bool) {
	p.healthChecksEnabled = enabled
	p.balancer = newBalancer(p.lbStrategy, p.targets, p.healthChecksEnabled)
}

// context key for cached request key
type cacheKeyCtxKey struct{}
type upstreamTargetCtxKey struct{}
// add context key for request start time (end-to-end measurement)
type startTimeCtxKey struct{}

// Checks if the client explicitly requested no-cache.
func clientNoCache(r *http.Request) bool {
	cc := parseCacheControl(r.Header.Get("Cache-Control"))
	if _, ok := cc["no-cache"]; ok {
		return true
	}
	if _, ok := cc["no-store"]; ok {
		return true
	}
	if strings.EqualFold(r.Header.Get("Pragma"), "no-cache") {
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
func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if sch := r.Header.Get("X-Forwarded-Proto"); sch != "" {
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

// sanitizeResponseHeaders returns a copy of h without hop-by-hop headers.
func sanitizeResponseHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vv := range h {
		// copy values
		for _, v := range vv {
			out.Add(k, v)
		}
	}
	for _, hh := range hopHeaders {
		out.Del(hh)
	}
	return out
}

// Wraps a response with its status and headers.
func respWithBody(status int, header http.Header) *http.Response {
	return &http.Response{StatusCode: status, Header: header}
}

