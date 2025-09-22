package proxy

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ReverseProxy struct {
	target    *url.URL
	transport *http.Transport
	cache     Cache
	cacheOn   bool
}

// Creates a new ReverseProxy instance with the specified target, cache, and cache toggle.
func NewReverseProxy(target *url.URL, cache Cache, cacheOn bool) *ReverseProxy {
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &ReverseProxy{
		target:    target,
		transport: tr,
		cache:     cache,
		cacheOn:   cacheOn,
	}
}

// Handles incoming HTTP requests and routes them to the appropriate target.
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Health check endpoint
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	ctx := r.Context()
	outreq := r.Clone(ctx)
	p.directRequest(outreq)

	// Bypass cache if requested by the client
	if p.cacheOn && isCacheableRequest(outreq) && !clientNoCache(outreq) {
		key := buildCacheKey(outreq)
		if cached, ok, stale := p.cache.Get(key); ok && !stale {
			// Write cached response
			copyHeader(w.Header(), cached.Header)
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(cached.StatusCode)
			_, _ = w.Write(cached.Body)
			return
		}
	}

	// No cache (miss/bypass): forward request to upstream
	resp, err := p.transport.RoundTrip(outreq)
	if err != nil {
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

	headers := filterHeaders(resp.Header)
	status := resp.StatusCode

	// Determine X-Cache header value
	eligibleReq := p.cacheOn && isCacheableRequest(outreq) && !clientNoCache(outreq)
	ttl, cacheableResp := isCacheableResponse(respWithBody(status, headers))
	xcache := "BYPASS"
	if eligibleReq && cacheableResp {
		xcache = "MISS"
	}

	// Write headers and body to the client
	copyHeader(w.Header(), headers)
	w.Header().Set("X-Cache", xcache)
	w.WriteHeader(status)
	_, _ = w.Write(buf)

	// Cache the response if eligible
	if eligibleReq && cacheableResp {
		key := buildCacheKey(outreq)
		p.cache.Set(key, &CachedResponse{
			StatusCode: status,
			Header:     headers,
			Body:       buf,
			StoredAt:   time.Now(),
		}, ttl)
	}
}

// Rewrites the request URL, path, and hop-by-hop headers.
func (p *ReverseProxy) directRequest(outreq *http.Request) {
	// Rewrite URL
	outreq.URL.Scheme = p.target.Scheme
	outreq.URL.Host = p.target.Host
	outreq.URL.Path = singleJoiningSlash(p.target.Path, outreq.URL.Path)

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
	outreq.Host = p.target.Host
}

// Determines the scheme of the request (http or https).
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

// Wraps a response with its status and headers.
func respWithBody(status int, header http.Header) *http.Response {
	return &http.Response{StatusCode: status, Header: header}
}

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