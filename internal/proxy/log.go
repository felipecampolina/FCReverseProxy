package proxy

import (
	"log"
	"net/http"
	"time"
)

// LogCacheHit logs details of a cache hit in the same pattern as upstream server logs.
func logRequestCacheHit(r *http.Request) {
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
func logResponseCacheHit(status int, bytes int, dur time.Duration, respHeaders http.Header, req *http.Request, resp http.ResponseWriter, notModified bool, respBodyNote string) {
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

