package proxy

import (
	"net/http"
	"sort"
	"strings"
)

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