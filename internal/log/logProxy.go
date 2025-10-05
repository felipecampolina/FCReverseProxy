package applog

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Loki client configuration and logging-level toggles.
//
// lokiURL: endpoint where logs are pushed.
// lokiOnce: ensures one-time Loki client initialization.
// lokiClient: short timeout HTTP client for fire-and-forget logging.
// infoEnabled/debugEnabled/errorEnabled: feature toggles for log levels.
// Note: Currently all are enabled by default.
var (
	lokiURL    string
	lokiOnce   sync.Once
	lokiClient = &http.Client{Timeout: 200 * time.Millisecond}

	// logging level toggles (currently: INFO/DEBUG/ERROR enabled)
	infoEnabled  = true
	debugEnabled = true
	errorEnabled = true
)

// LogProxyRequest logs a proxy request before it is served by an upstream (i.e., not a cache hit).
// It emits:
// - info: concise, high-level request metadata
// - debug: detailed request context including headers
func LogProxyRequest(req *http.Request) {
	// Detailed line for debug-level visibility (includes headers and proto).
	debugLine := fmt.Sprintf(
		"REQ remote=%s method=%s url=%s proto=%s req-content-length=%s headers=%v",
		req.RemoteAddr,
		req.Method,
		req.URL.RequestURI(),
		req.Proto,
		req.Header.Get("Content-Length"),
		req.Header,
	)

	requestURI := req.URL.RequestURI()
	upstreamName := req.Header.Get("X-Upstream")
	if strings.TrimSpace(upstreamName) == "" {
		upstreamName = "unknown"
	}

	// Common Loki labels for log correlation and filtering.
	labels := map[string]string{
		"method":     req.Method,
		"status":     "pending",
		"cache":      "MISS",
		"upstream":   upstreamName,
		"host":       MustHostname(),
		"request_id": req.Header.Get("X-Request-ID"),
		"url":        requestURI,
	}

	// INFO: concise line suitable for dashboards/metrics correlation.
	infoLine := fmt.Sprintf("REQ method=%s url=%s | cache=MISS req_id=%s", req.Method, requestURI, req.Header.Get("X-Request-ID"))
	Emit("info", "proxy", labels, infoLine)

	// DEBUG: full context including headers.
	Emit("debug", "proxy", labels, debugLine)
}

// LogProxyError emits an error-level log for proxy failures
// (e.g., 5xx from upstream, no healthy targets, timeouts, etc.).
func LogProxyError(status int, cacheLabel string, upstreamName string, req *http.Request, err error) {
	requestURI := req.URL.RequestURI()

	if strings.TrimSpace(upstreamName) == "" {
		upstreamName = "unknown"
	}

	labels := map[string]string{
		"method":     req.Method,
		"status":     strconv.Itoa(status),
		"cache":      cacheLabel,
		"upstream":   upstreamName,
		"host":       MustHostname(),
		"request_id": req.Header.Get("X-Request-ID"),
		"url":        requestURI,
	}

	errorLine := fmt.Sprintf(
		"ERROR status=%d method=%s url=%s upstream=%s cache=%s err=%v req_id=%s",
		status, req.Method, requestURI, upstreamName, cacheLabel, err, req.Header.Get("X-Request-ID"),
	)
	Emit("error", "proxy", labels, errorLine)
}

// LogProxyRequestCacheHit logs a request that is served from cache before responding.
// It mirrors upstream server logs but marks the event as a cache HIT.
func LogProxyRequestCacheHit(req *http.Request) {
	// Detailed line for debug-level visibility (includes headers and proto).
	debugLine := fmt.Sprintf(
		"REQ remote=%s method=%s url=%s proto=%s req-content-length=%s headers=%v | CACHE HIT",
		req.RemoteAddr,
		req.Method,
		req.URL.RequestURI(),
		req.Proto,
		req.Header.Get("Content-Length"),
		req.Header,
	)

	requestURI := req.URL.RequestURI()
	upstreamName := req.Header.Get("X-Upstream")
	if strings.TrimSpace(upstreamName) == "" {
		upstreamName = "unknown"
	}

	labels := map[string]string{
		"method":     req.Method,
		"status":     "200",
		"cache":      "HIT",
		"upstream":   upstreamName,
		"host":       MustHostname(),
		"request_id": req.Header.Get("X-Request-ID"),
		"url":        requestURI,
	}

	// INFO: concise cache-hit indicator
	infoLine := fmt.Sprintf("REQ method=%s url=%s | cache=HIT req_id=%s", req.Method, requestURI, req.Header.Get("X-Request-ID"))
	Emit("info", "proxy", labels, infoLine)

	// DEBUG: full request context on cache HIT
	Emit("debug", "proxy", labels, debugLine)
}

// LogProxyResponseCacheHit logs the final response, including cache metadata from response headers.
// It also aligns labels for log correlation. This is used for cache HITs and revalidations.
// respHeaders should include cache-related headers (Cache-Control, ETag, Age, Via, X-Cache).
func LogProxyResponseCacheHit(
	status int,
	bytesWritten int,
	duration time.Duration,
	respHeaders http.Header,
	req *http.Request,
	_ http.ResponseWriter,
	notModified bool,
	respBodyNote string,
) {
	// Detailed line for debug-level visibility (includes request/response cache controls).
	debugLine := fmt.Sprintf(
		"RESP status=%d bytes=%d dur=%s resp-content-length=%s resp_headers=%v | cache:req_cc=%v resp_cc=%v etag=%q last-modified=%q expires=%q age=%q via=%q x-cache=%q not-modified=%t%s",
		status,
		bytesWritten,
		duration.String(),
		respHeaders.Get("Content-Length"),
		respHeaders,
		// parseCacheControlList returns a normalized list of Cache-Control directives.
		parseCacheControlList(req.Header.Get("Cache-Control")),
		parseCacheControlList(respHeaders.Get("Cache-Control")),
		respHeaders.Get("ETag"),
		respHeaders.Get("Last-Modified"),
		respHeaders.Get("Expires"),
		respHeaders.Get("Age"),
		respHeaders.Get("Via"),
		respHeaders.Get("X-Cache"),
		notModified,
		respBodyNote,
	)

	cacheLabel := respHeaders.Get("X-Cache")
	upstreamName := respHeaders.Get("X-Upstream")
	if strings.TrimSpace(upstreamName) == "" {
		upstreamName = "unknown"
	}
	requestURI := req.URL.RequestURI()

	labels := map[string]string{
		"method":     req.Method,
		"status":     strconv.Itoa(status),
		"cache":      cacheLabel,
		"upstream":   upstreamName,
		"host":       MustHostname(),
		"request_id": req.Header.Get("X-Request-ID"),
		"url":        requestURI,
	}

	// INFO: concise response summary
	infoLine := fmt.Sprintf(
		"RESP status=%d bytes=%d dur=%s cache=%s upstream=%s req_id=%s",
		status, bytesWritten, duration.String(), cacheLabel, upstreamName, req.Header.Get("X-Request-ID"),
	)
	Emit("info", "proxy", labels, infoLine)

	// DEBUG: full response and cache diagnostic context
	Emit("debug", "proxy", labels, debugLine)

	// NEW: Emit error-level for any 4xx/5xx so Promtail/Loki capture them.
	if status >= 400 {
		errLine := fmt.Sprintf(
			"ERROR status=%d bytes=%d dur=%s cache=%s upstream=%s req_id=%s",
			status, bytesWritten, duration.String(), cacheLabel, upstreamName, req.Header.Get("X-Request-ID"),
		)
		// Include response body preview when available.
		if strings.TrimSpace(respBodyNote) != "" {
			errLine = errLine + " " + strings.TrimSpace(respBodyNote)
		}
		Emit("error", "proxy", labels, errLine)
	}
}

