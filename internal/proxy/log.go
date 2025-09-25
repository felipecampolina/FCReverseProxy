package proxy

import (
	"flag"
	"log"
	"net/http"
	"time"

	imetrics "traefik-challenge-2/internal/metrics"
)

func logEnabled() bool {
	// In test binaries, the testing package registers these flags.
	if flag.Lookup("test.v") != nil || flag.Lookup("test.run") != nil || flag.Lookup("test.bench") != nil {
		return false
	}
	return true
}

// LogCacheHit logs details of a cache hit in the same pattern as upstream server logs.
func logRequestCacheHit(r *http.Request) {
	if !logEnabled() {
		return
	}
	log.Printf(
		"REQ remote=%s method=%s url=%s proto=%s req-content-length=%s headers=%v | CACHE HIT",
		r.RemoteAddr,
		r.Method,
		r.URL.RequestURI(),
		r.Proto,
		r.Header.Get("Content-Length"),
		r.Header,
	)
}

// LogResponse logs details of a response in the specified pattern.
// Also records Prometheus metrics using response headers (including X-Cache).
func logResponseCacheHit(status int, bytes int, dur time.Duration, respHeaders http.Header, req *http.Request, resp http.ResponseWriter, notModified bool, respBodyNote string) {
	if !logEnabled() {
		// still record metrics in non-verbose mode
	} else {
		log.Printf(
			"RESP status=%d bytes=%d dur=%s resp-content-length=%s resp_headers=%v | cache:req_cc=%v resp_cc=%v etag=%q last-modified=%q expires=%q age=%q via=%q x-cache=%q not-modified=%t%s",
			status,
			bytes,
			dur.String(), // Use the dynamically passed duration
			respHeaders.Get("Content-Length"), // Use respHeaders for Content-Length
			respHeaders,
			parseCacheControl(req.Header.Get("Cache-Control")),
			parseCacheControl(respHeaders.Get("Cache-Control")), // Use respHeaders for Cache-Control
			respHeaders.Get("ETag"),
			respHeaders.Get("Last-Modified"),
			respHeaders.Get("Expires"),
			respHeaders.Get("Age"),
			respHeaders.Get("Via"),
			respHeaders.Get("X-Cache"),
			notModified,
			respBodyNote,
		)
	}

	cacheLabel := respHeaders.Get("X-Cache")
	imetrics.ObserveProxyResponse(req.Method, status, cacheLabel, dur)
}
