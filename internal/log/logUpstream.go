package applog

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	imetrics "traefik-challenge-2/internal/metrics"
)

// loggingResponseWriter wraps http.ResponseWriter to capture response status,
// number of bytes written, and a small preview of the response body for debugging.
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode    int    // HTTP status code sent to the client
	bytesWritten  int    // number of bytes written to the client
	respPreview   []byte // small response body preview (for logs only)
	respMaxPreview int   // max number of bytes to store in respPreview
}

func (w *loggingResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	// Default status code to 200 if WriteHeader was never called.
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}

	// Capture a preview of the response body up to the configured limit.
	if w.respMaxPreview > 0 && len(w.respPreview) < w.respMaxPreview {
		remaining := w.respMaxPreview - len(w.respPreview)
		if remaining > 0 {
			copyLen := len(b)
			if copyLen > remaining {
				copyLen = remaining
			}
			w.respPreview = append(w.respPreview, b[:copyLen]...)
		}
	}

	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n
	return n, err
}

// readCloserCombiner allows us to replace r.Body with a new reader while keeping
// the original Closer to ensure the underlying body is still closed properly.
type readCloserCombiner struct {
	io.Reader
	closer io.Closer
}

func (r readCloserCombiner) Close() error { return r.closer.Close() }

// WithRequestLogging logs request/response details and records metrics.
// - Skips Prometheus scrapes (identified by isMetricsScrape).
// - Previews up to 8KB of request/response bodies for debugging.
// - Emits both concise and detailed log lines with labels.
func WithRequestLogging(next http.Handler) http.Handler {
	const maxBodyPreview = 8 << 10 // 8KB body preview limit

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fast-path: avoid overhead for Prometheus scrapes.
		if isMetricsScrape(r) {
			next.ServeHTTP(w, r)
			return
		}

		startTime := time.Now()
		imetrics.UpstreamInflightInc()
		defer imetrics.UpstreamInflightDec()

		// Determine client IP. Prefer X-Forwarded-For's first hop if present.
		var clientIP, forwardedForChain string
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			forwardedForChain = xf
			clientIP = strings.TrimSpace(strings.Split(xf, ",")[0])
		} else {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				clientIP = r.RemoteAddr
			} else {
				clientIP = host
			}
		}

		previewLimit := maxBodyPreview // request body preview limit

		// Safely preview up to previewLimit bytes of the request body and restore it.
		var reqBodyPreview []byte
		if r.Body != nil && previewLimit > 0 {
			// Read up to limit+1 to detect truncation without storing >limit.
			limited := io.LimitReader(r.Body, int64(previewLimit+1))
			buf, _ := io.ReadAll(limited)
			reqBodyTruncated := len(buf) > previewLimit
			if reqBodyTruncated {
				reqBodyPreview = buf[:previewLimit]
			} else {
				reqBodyPreview = buf
			}

			// Rebuild r.Body so downstream handlers can read it fully:
			// - If truncated: replay the preview + remaining original body
			// - Else: we consumed it all; just replay the preview
			originalBody := r.Body
			var combinedReader io.Reader = bytes.NewReader(reqBodyPreview)
			if reqBodyTruncated {
				combinedReader = io.MultiReader(bytes.NewReader(reqBodyPreview), originalBody)
			} else {
				// No remainder; ensure Close is still safe to call.
				originalBody = io.NopCloser(bytes.NewReader(nil))
			}
			r.Body = readCloserCombiner{Reader: combinedReader, closer: originalBody}
		}

		// Flatten request headers for logging readability.
		requestHeaders := make(map[string]string, len(r.Header))
		for k, v := range r.Header {
			if len(v) == 1 {
				requestHeaders[k] = v[0]
			} else {
				requestHeaders[k] = strings.Join(v, ", ")
			}
		}

		reqBodyNote := ""
		if len(reqBodyPreview) > 0 {
			reqBodyNote = fmt.Sprintf(", req_body_preview=%q", string(reqBodyPreview))
		}

		// Request summary (detailed)
		reqLine := fmt.Sprintf(
			"REQ remote=%s fwd=%q method=%s url=%s proto=%s req-content-length=%s headers=%v%s",
			clientIP,
			forwardedForChain,
			r.Method,
			r.URL.RequestURI(),
			r.Proto,
			r.Header.Get("Content-Length"),
			requestHeaders,
			reqBodyNote,
		)

		upstreamHeaderValue := strings.TrimSpace(r.Header.Get("X-Upstream"))
		if upstreamHeaderValue == "" {
			upstreamHeaderValue = "unknown"
		}

		// Common labels for request logs
		requestLabels := map[string]string{
			"method":     r.Method,
			"status":     "pending",
			"cache":      "",
			"upstream":   upstreamHeaderValue,
			"host":       MustHostname(),
			"request_id": r.Header.Get("X-Request-ID"),
			"url":        r.URL.RequestURI(),
		}

		// INFO (concise) + DEBUG (detailed) request logs
		infoReqMsg := fmt.Sprintf("REQ method=%s url=%s req_id=%s", r.Method, r.URL.RequestURI(), r.Header.Get("X-Request-ID"))
		Emit("info", "upstream", requestLabels, infoReqMsg)
		Emit("debug", "upstream", requestLabels, reqLine)

		// Wrap ResponseWriter to capture status, bytes written, and response preview.
		logWriter := &loggingResponseWriter{
			ResponseWriter:  w,
			respMaxPreview:  previewLimit,
		}

		// Pass request to the next handler.
		next.ServeHTTP(logWriter, r)

		// Compute request duration after handler completes.
		duration := time.Since(startTime)

		// Flatten response headers for logging readability.
		responseHeaders := make(map[string]string, len(logWriter.Header()))
		for k, v := range logWriter.Header() {
			if len(v) == 1 {
				responseHeaders[k] = v[0]
			} else {
				responseHeaders[k] = strings.Join(v, ", ")
			}
		}

		// Cache-related request/response header tokens.
		reqCacheCC := parseCacheControlList(r.Header.Get("Cache-Control"))
		respCacheCC := parseCacheControlList(logWriter.Header().Get("Cache-Control"))
		notModified := logWriter.statusCode == http.StatusNotModified

		// Selected response headers
		respContentLength := logWriter.Header().Get("Content-Length")
		respETag := logWriter.Header().Get("ETag")
		respLastModified := logWriter.Header().Get("Last-Modified")
		respExpires := logWriter.Header().Get("Expires")
		respAge := logWriter.Header().Get("Age")
		respVia := logWriter.Header().Get("Via")
		respXCache := logWriter.Header().Get("X-Cache")

		respBodyNote := ""
		if len(logWriter.respPreview) > 0 {
			respBodyNote = fmt.Sprintf(", resp_body_preview=%q", string(logWriter.respPreview))
		}

		// Response summary (detailed)
		respLine := fmt.Sprintf(
			"RESP status=%d bytes=%d dur=%s resp-content-length=%s resp_headers=%v | cache:req_cc=%v resp_cc=%v etag=%q last-modified=%q expires=%q age=%q via=%q x-cache=%q not-modified=%t%s",
			logWriter.statusCode,
			logWriter.bytesWritten,
			duration.String(),
			respContentLength,
			responseHeaders,
			reqCacheCC,
			respCacheCC,
			respETag,
			respLastModified,
			respExpires,
			respAge,
			respVia,
			respXCache,
			notModified,
			respBodyNote,
		)

		// Fallback to 200 if no explicit status was written.
		respStatus := logWriter.statusCode
		if respStatus == 0 {
			respStatus = http.StatusOK
		}

		upstreamID := strings.TrimSpace(logWriter.Header().Get("X-Upstream"))
		if upstreamID == "" {
			upstreamID = "unknown"
		}

		// Common labels for response logs
		responseLabels := map[string]string{
			"method":     r.Method,
			"status":     strconv.Itoa(respStatus),
			"cache":      respXCache,
			"upstream":   upstreamID,
			"host":       MustHostname(),
			"request_id": r.Header.Get("X-Request-ID"),
			"url":        r.URL.RequestURI(),
		}

		// INFO (concise) + DEBUG (detailed) response logs
		infoRespMsg := fmt.Sprintf("RESP status=%d bytes=%d dur=%s upstream=%s req_id=%s", respStatus, logWriter.bytesWritten, duration.String(), upstreamID, r.Header.Get("X-Request-ID"))
		Emit("info", "upstream", responseLabels, infoRespMsg)
		Emit("debug", "upstream", responseLabels, respLine)

		// NEW: Emit an error-level log for any 4xx/5xx response.
		// This ensures errors like "invalid JSON body" appear in Loki/Promtail.
		if respStatus >= 400 {
			errPreview := ""
			if len(logWriter.respPreview) > 0 {
				errPreview = fmt.Sprintf(" resp_body_preview=%q", string(logWriter.respPreview))
			}
			errorLine := fmt.Sprintf(
				"ERROR status=%d method=%s url=%s dur=%s upstream=%s req_id=%s%s",
				respStatus,
				r.Method,
				r.URL.RequestURI(),
				duration.String(),
				upstreamID,
				r.Header.Get("X-Request-ID"),
				errPreview,
			)
			Emit("error", "upstream", responseLabels, errorLine)
		}

		// Record Prometheus metrics for the upstream call.
		imetrics.ObserveUpstreamResponse(r.Method, respStatus, duration)
	})
}

var requestCounter int64

// WithRequestID ensures each request has a stable X-Request-ID used by logs/metrics.
// - If the header already exists, it is preserved.
// - Emits pre/post debug lines keyed by the request ID.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Do nothing for Prometheus scrapes.
		if isMetricsScrape(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Respect existing X-Request-ID; generate one only if missing.
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&requestCounter, 1))
			r.Header.Set("X-Request-ID", requestID)
		}

		preLogLine := fmt.Sprintf("REQ_ID=%s method=%s url=%s", requestID, r.Method, r.URL.Path)

		upstreamHeader := strings.TrimSpace(r.Header.Get("X-Upstream"))
		if upstreamHeader == "" {
			upstreamHeader = "unknown"
		}

		// DEBUG (pre-handler)
		Emit("debug", "upstream", map[string]string{
			"request_id": requestID,
			"method":     r.Method,
			"host":       MustHostname(),
			"url":        r.URL.Path,
			"status":     "pending",
			"cache":      "",
			"upstream":   upstreamHeader,
		}, preLogLine)

		next.ServeHTTP(w, r)

		postLogLine := fmt.Sprintf("REQ_ID=%s completed", requestID)

		// DEBUG (post-handler)
		Emit("debug", "upstream", map[string]string{
			"request_id": requestID,
			"method":     r.Method,
			"host":       MustHostname(),
			"url":        r.URL.Path,
			"status":     "",
			"cache":      "",
			"upstream":   upstreamHeader,
		}, postLogLine)
	})
}
