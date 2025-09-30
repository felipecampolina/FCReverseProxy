package applog

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	imetrics "traefik-challenge-2/internal/metrics"

	"gopkg.in/yaml.v3"
)

var (
	lokiURL    string
	lokiOnce   sync.Once
	lokiClient = &http.Client{Timeout: 200 * time.Millisecond}

	// logging level toggles (defaults: INFO/ERROR on, DEBUG off)
	infoEnabled  = true
	debugEnabled = false
	errorEnabled = true
)

func initLoki() {
	lokiURL = ""

	// Prefer configs/config.yaml|yml
	cfgFile := ""
	for _, c := range []string{"configs/config.yaml", "configs/config.yml"} {
		if _, err := os.Stat(c); err == nil {
			cfgFile = c
			break
		}
	}
	if cfgFile != "" {
		var cfg struct {
			Metrics *struct {
				LokiURL string `yaml:"loki_url"`
			} `yaml:"metrics"`
			Logging *struct {
				InfoEnabled  *bool `yaml:"info_enabled"`
				DebugEnabled *bool `yaml:"debug_enabled"`
				ErrorEnabled *bool `yaml:"error_enabled"`
			} `yaml:"logging"`
		}
		if b, err := os.ReadFile(cfgFile); err == nil {
			if err := yaml.Unmarshal(b, &cfg); err == nil {
				if cfg.Metrics != nil && strings.TrimSpace(cfg.Metrics.LokiURL) != "" {
					lokiURL = strings.TrimSpace(cfg.Metrics.LokiURL)
				}
				// Apply logging level toggles if present
				if cfg.Logging != nil {
					if cfg.Logging.InfoEnabled != nil {
						infoEnabled = *cfg.Logging.InfoEnabled
					}
					if cfg.Logging.DebugEnabled != nil {
						debugEnabled = *cfg.Logging.DebugEnabled
					}
					if cfg.Logging.ErrorEnabled != nil {
						errorEnabled = *cfg.Logging.ErrorEnabled
					}
				}
			}
		}
	}

	// Normalize to full push path if base URL provided
	if lokiURL != "" && !strings.Contains(lokiURL, "/loki/api/v1/push") {
		lokiURL = strings.TrimRight(lokiURL, "/") + "/loki/api/v1/push"
	}
}

// levelEnabled reports if a given log level is enabled according to config.
func levelEnabled(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return debugEnabled
	case "error":
		return errorEnabled
	default:
		return infoEnabled
	}
}

// Emit prints locally (if enabled) and pushes the same line to Loki with a "level" label.
func Emit(level, app string, labels map[string]string, line string) {
	lvl := strings.ToLower(level)
	// Local print (skip during tests)
	if logEnabled() && levelEnabled(lvl) {
		log.Print(line)
	}
	// Loki
	PushLokiWithLevel(lvl, app, labels, line)
}

// PushLokiWithLevel sends a single log line with labels to Loki, adding a "level" label.
// No-op if Loki is not configured or the level is disabled.
func PushLokiWithLevel(level, app string, labels map[string]string, line string) {
	lokiOnce.Do(initLoki)
	if lokiURL == "" || !levelEnabled(level) {
		return
	}

	lbls := map[string]string{
		"app":   app,
		"level": strings.ToLower(strings.TrimSpace(level)),
	}
	for k, v := range labels {
		if strings.TrimSpace(k) == "" {
			continue
		}
		lbls[k] = v
	}

	ts := strconv.FormatInt(time.Now().UnixNano(), 10)
	payload := struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"streams"`
	}{
		Streams: []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		}{
			{Stream: lbls, Values: [][2]string{{ts, line}}},
		},
	}

	b, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", lokiURL, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	_, _ = lokiClient.Do(req) // fire-and-forget
}

// Backward-compatible helper (defaults to INFO level).
func PushLoki(app string, labels map[string]string, line string) {
	PushLokiWithLevel("INFO", app, labels, line)
}

// MustHostname returns the current hostname or "unknown" on error.
func MustHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

// ------------- shared helpers ------------

func logEnabled() bool {
	// In test binaries, the testing package registers these flags.
	if flag.Lookup("test.v") != nil || flag.Lookup("test.run") != nil || flag.Lookup("test.bench") != nil {
		return false
	}
	return true
}

func parseCacheControlList(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isMetricsScrape(r *http.Request) bool {
	if r.URL != nil && r.URL.Path == "/metrics" {
		return true
	}
	if strings.Contains(r.Header.Get("User-Agent"), "Prometheus") {
		return true
	}
	if strings.Contains(r.Header.Get("Accept"), "openmetrics") {
		return true
	}
	return false
}

// ------------- proxy logging ------------

// LogProxyRequest logs a proxy request (non-cache-hit) with INFO and DEBUG entries.
func LogProxyRequest(r *http.Request) {
	line := fmt.Sprintf(
		"REQ remote=%s method=%s url=%s proto=%s req-content-length=%s headers=%v",
		r.RemoteAddr,
		r.Method,
		r.URL.RequestURI(),
		r.Proto,
		r.Header.Get("Content-Length"),
		r.Header,
	)

	url := r.URL.RequestURI()
	up := r.Header.Get("X-Upstream")
	if strings.TrimSpace(up) == "" {
		up = "unknown"
	}
	labels := map[string]string{
		"method":     r.Method,
		"status":     "pending",
		"cache":      "MISS",
		"upstream":   up,
		"host":       MustHostname(),
		"request_id": r.Header.Get("X-Request-ID"),
		"url":        url,
	}

	// INFO (generic)
	infoLine := fmt.Sprintf("REQ method=%s url=%s | cache=MISS req_id=%s", r.Method, url, r.Header.Get("X-Request-ID"))
	Emit("info", "proxy", labels, infoLine)

	// DEBUG (detailed)
	Emit("debug", "proxy", labels, line)
}

// LogProxyError emits an error-level log for proxy failures (e.g., upstream errors or no healthy targets).
func LogProxyError(status int, cacheLabel string, upstream string, r *http.Request, err error) {
	url := r.URL.RequestURI()
	if strings.TrimSpace(upstream) == "" {
		upstream = "unknown"
	}
	labels := map[string]string{
		"method":     r.Method,
		"status":     strconv.Itoa(status),
		"cache":      cacheLabel,
		"upstream":   upstream,
		"host":       MustHostname(),
		"request_id": r.Header.Get("X-Request-ID"),
		"url":        url,
	}
	line := fmt.Sprintf("ERROR status=%d method=%s url=%s upstream=%s cache=%s err=%v req_id=%s",
		status, r.Method, url, upstream, cacheLabel, err, r.Header.Get("X-Request-ID"))
	Emit("error", "proxy", labels, line)
}

// LogProxyRequestCacheHit logs details of a cache hit in the same pattern as upstream server logs.
func LogProxyRequestCacheHit(r *http.Request) {
	line := fmt.Sprintf(
		"REQ remote=%s method=%s url=%s proto=%s req-content-length=%s headers=%v | CACHE HIT",
		r.RemoteAddr,
		r.Method,
		r.URL.RequestURI(),
		r.Proto,
		r.Header.Get("Content-Length"),
		r.Header,
	)

	url := r.URL.RequestURI()
	up := r.Header.Get("X-Upstream")
	if strings.TrimSpace(up) == "" {
		up = "unknown"
	}
	labels := map[string]string{
		"method":     r.Method,
		"status":     "200",
		"cache":      "HIT",
		"upstream":   up,
		"host":       MustHostname(),
		"request_id": r.Header.Get("X-Request-ID"),
		"url":        url,
	}

	// INFO (generic)
	infoLine := fmt.Sprintf("REQ method=%s url=%s | cache=HIT req_id=%s", r.Method, url, r.Header.Get("X-Request-ID"))
	Emit("info", "proxy", labels, infoLine)

	// DEBUG (detailed)
	Emit("debug", "proxy", labels, line)
}

// LogProxyResponseCacheHit logs details of a response and records Prometheus metrics using response headers (including X-Cache).
func LogProxyResponseCacheHit(status int, bytes int, dur time.Duration, respHeaders http.Header, req *http.Request, _ http.ResponseWriter, notModified bool, respBodyNote string) {
	line := fmt.Sprintf(
		"RESP status=%d bytes=%d dur=%s resp-content-length=%s resp_headers=%v | cache:req_cc=%v resp_cc=%v etag=%q last-modified=%q expires=%q age=%q via=%q x-cache=%q not-modified=%t%s",
		status,
		bytes,
		dur.String(),
		respHeaders.Get("Content-Length"),
		respHeaders,
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
	up := respHeaders.Get("X-Upstream")
	if strings.TrimSpace(up) == "" {
		up = "unknown"
	}
	url := req.URL.RequestURI()

	labels := map[string]string{
		"method":     req.Method,
		"status":     strconv.Itoa(status),
		"cache":      cacheLabel,
		"upstream":   up,
		"host":       MustHostname(),
		"request_id": req.Header.Get("X-Request-ID"),
		"url":        url,
	}

	// INFO (generic)
	infoLine := fmt.Sprintf("RESP status=%d bytes=%d dur=%s cache=%s upstream=%s req_id=%s", status, bytes, dur.String(), cacheLabel, up, req.Header.Get("X-Request-ID"))
	Emit("info", "proxy", labels, infoLine)

	// DEBUG (detailed)
	Emit("debug", "proxy", labels, line)
}

// ------------- upstream logging ------------

// loggingResponseWriter captures status code and bytes written.
type loggingResponseWriter struct {
	http.ResponseWriter
	status     int
	n          int
	preview    []byte // response body preview
	maxPreview int    // max preview bytes
}

func (w *loggingResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	// Capture response preview up to maxPreview
	if w.maxPreview > 0 && len(w.preview) < w.maxPreview {
		rem := w.maxPreview - len(w.preview)
		if rem > 0 {
			cp := len(b)
			if cp > rem {
				cp = rem
			}
			w.preview = append(w.preview, b[:cp]...)
		}
	}
	n, err := w.ResponseWriter.Write(b)
	w.n += n
	return n, err
}

// rcCombiner lets us restore a body while still closing the original.
type rcCombiner struct {
	io.Reader
	closer io.Closer
}

func (r rcCombiner) Close() error { return r.closer.Close() }

// WithRequestLogging logs request/response details for every request and emits Loki + Prometheus metrics.
func WithRequestLogging(next http.Handler) http.Handler {
	const maxBodyPreview = 8 << 10 // 8KB
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fast-path: do not log or record metrics for Prometheus scrapes.
		if isMetricsScrape(r) {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		imetrics.UpstreamInflightInc()
		defer imetrics.UpstreamInflightDec()

		// Prepare remote address (favor X-Forwarded-For if present).
		var remote, fwdChain string
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			fwdChain = xf
			remote = strings.TrimSpace(strings.Split(xf, ",")[0])
		} else {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				remote = r.RemoteAddr
			} else {
				remote = host
			}
		}

		// Always preview up to maxBodyPreview for non-scrape requests
		previewLimit := maxBodyPreview

		// Safely preview up to previewLimit of the body and restore it.
		var preview []byte
		if r.Body != nil && previewLimit > 0 {
			limited := io.LimitReader(r.Body, int64(previewLimit+1))
			buf, _ := io.ReadAll(limited)
			truncated := len(buf) > previewLimit
			if truncated {
				preview = buf[:previewLimit]
			} else {
				preview = buf
			}
			// Restore the body so handlers can read full content.
			rest := r.Body // remaining unread portion (if any)
			reader := io.Reader(bytes.NewReader(preview))
			if truncated {
				reader = io.MultiReader(bytes.NewReader(preview), rest)
			} else {
				// We fully consumed available bytes; no remainder
				reader = bytes.NewReader(preview)
				rest = io.NopCloser(bytes.NewReader(nil)) // dummy closer
			}
			r.Body = rcCombiner{Reader: reader, closer: rest}
		}

		// Compact headers map for request logging.
		reqHeaders := make(map[string]string, len(r.Header))
		for k, v := range r.Header {
			if len(v) == 1 {
				reqHeaders[k] = v[0]
			} else {
				reqHeaders[k] = strings.Join(v, ", ")
			}
		}

		bodyNote := ""
		if len(preview) > 0 {
			bodyNote = fmt.Sprintf(", req_body_preview=%q", string(preview))
		}

		// Log request line
		reqLine := fmt.Sprintf(
			"REQ remote=%s fwd=%q method=%s url=%s proto=%s req-content-length=%s headers=%v%s",
			remote,
			fwdChain,
			r.Method,
			r.URL.RequestURI(),
			r.Proto,
			r.Header.Get("Content-Length"),
			reqHeaders,
			bodyNote,
		)

		upReq := r.Header.Get("X-Upstream")
		if strings.TrimSpace(upReq) == "" {
			upReq = "unknown"
		}

		// Common labels for upstream logs
		reqLabels := map[string]string{
			"method":     r.Method,
			"status":     "pending",
			"cache":      "",
			"upstream":   upReq,
			"host":       MustHostname(),
			"request_id": r.Header.Get("X-Request-ID"),
			"url":        r.URL.RequestURI(),
		}

		// INFO (generic)
		infoReq := fmt.Sprintf("REQ method=%s url=%s req_id=%s", r.Method, r.URL.RequestURI(), r.Header.Get("X-Request-ID"))
		Emit("info", "upstream", reqLabels, infoReq)

		// DEBUG (detailed)
		Emit("debug", "upstream", reqLabels, reqLine)

		// Wrap ResponseWriter to capture status/bytes and preview body (limited by previewLimit)
		lrw := &loggingResponseWriter{ResponseWriter: w, maxPreview: previewLimit}
		next.ServeHTTP(lrw, r)

		dur := time.Since(start)

		// Response headers map
		respHeaders := make(map[string]string, len(lrw.Header()))
		for k, v := range lrw.Header() {
			if len(v) == 1 {
				respHeaders[k] = v[0]
			} else {
				respHeaders[k] = strings.Join(v, ", ")
			}
		}

		// Cache-related details.
		reqCC := parseCacheControlList(r.Header.Get("Cache-Control"))
		respCC := parseCacheControlList(lrw.Header().Get("Cache-Control"))
		notModified := lrw.status == http.StatusNotModified

		// Response cache headers
		respCL := lrw.Header().Get("Content-Length")
		respETag := lrw.Header().Get("ETag")
		respLM := lrw.Header().Get("Last-Modified")
		respExp := lrw.Header().Get("Expires")
		respAge := lrw.Header().Get("Age")
		respVia := lrw.Header().Get("Via")
		respXCache := lrw.Header().Get("X-Cache")

		respBodyNote := ""
		if len(lrw.preview) > 0 {
			respBodyNote = fmt.Sprintf(", resp_body_preview=%q", string(lrw.preview))
		}

		// Log response line
		respLine := fmt.Sprintf(
			"RESP status=%d bytes=%d dur=%s resp-content-length=%s resp_headers=%v | cache:req_cc=%v resp_cc=%v etag=%q last-modified=%q expires=%q age=%q via=%q x-cache=%q not-modified=%t%s",
			lrw.status,
			lrw.n,
			dur.String(),
			respCL,
			respHeaders,
			reqCC,
			respCC,
			respETag,
			respLM,
			respExp,
			respAge,
			respVia,
			respXCache,
			notModified,
			respBodyNote,
		)

		// DEBUG/INFO emissions with same labels
		status := lrw.status
		if status == 0 {
			status = http.StatusOK
		}
		upID := lrw.Header().Get("X-Upstream")
		if strings.TrimSpace(upID) == "" {
			upID = "unknown"
		}
		respLabels := map[string]string{
			"method":     r.Method,
			"status":     strconv.Itoa(status),
			"cache":      respXCache,
			"upstream":   upID,
			"host":       MustHostname(),
			"request_id": r.Header.Get("X-Request-ID"),
			"url":        r.URL.RequestURI(),
		}

		// INFO (generic)
		infoResp := fmt.Sprintf("RESP status=%d bytes=%d dur=%s upstream=%s req_id=%s", status, lrw.n, dur.String(), upID, r.Header.Get("X-Request-ID"))
		Emit("info", "upstream", respLabels, infoResp)

		// DEBUG (detailed)
		Emit("debug", "upstream", respLabels, respLine)

		// Metrics
		imetrics.ObserveUpstreamResponse(r.Method, status, dur)
	})
}

var requestCounter int64

// WithRequestID assigns a unique ID to each request and includes it in the logs.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip logging/headers for Prometheus scrapes
		if isMetricsScrape(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Respect existing X-Request-ID (e.g., set by proxy); only create if missing.
		reqID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if reqID == "" {
			reqID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&requestCounter, 1))
			r.Header.Set("X-Request-ID", reqID)
		}
		preLine := fmt.Sprintf("REQ_ID=%s method=%s url=%s", reqID, r.Method, r.URL.Path)

		upHdr := r.Header.Get("X-Upstream")
		if strings.TrimSpace(upHdr) == "" {
			upHdr = "unknown"
		}

		// DEBUG
		Emit("debug", "upstream", map[string]string{
			"request_id": reqID,
			"method":     r.Method,
			"host":       MustHostname(),
			"url":        r.URL.Path,
			"status":     "pending",
			"cache":      "",
			"upstream":   upHdr,
		}, preLine)

		next.ServeHTTP(w, r)

		postLine := fmt.Sprintf("REQ_ID=%s completed", reqID)

		// DEBUG
		Emit("debug", "upstream", map[string]string{
			"request_id": reqID,
			"method":     r.Method,
			"host":       MustHostname(),
			"url":        r.URL.Path,
			"status":     "",
			"cache":      "",
			"upstream":   upHdr,
		}, postLine)
	})
}
