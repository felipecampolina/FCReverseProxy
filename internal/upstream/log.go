package upstream

import (
	"bytes"
	"encoding/json"
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

// loggingResponseWriter captures status code and bytes written.
type loggingResponseWriter struct {
	http.ResponseWriter
	status     int
	n          int
	preview    []byte   // response body preview
	maxPreview int      // max preview bytes
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

// withRequestLogging logs request/response details for every request.
func withRequestLogging(next http.Handler) http.Handler {
	const maxBodyPreview = 8 << 10 // 8KB
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fast-path: do not log or record metrics for Prometheus scrapes.
		// This aligns with proxy behavior where /metrics is not proxied/measured.
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
		log.Print(reqLine)
		// Push the exact same request line to Loki
		pushLoki("upstream", map[string]string{
			"method":     r.Method,
			"host":       mustHostname(),
			"request_id": r.Header.Get("X-Request-ID"),
		}, reqLine)

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
		reqCC := parseCacheControl(r.Header.Get("Cache-Control"))
		respCC := parseCacheControl(lrw.Header().Get("Cache-Control"))
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
		log.Print(respLine)

		// Metrics
		status := lrw.status
		if status == 0 {
			status = http.StatusOK
		}
		imetrics.ObserveUpstreamResponse(r.Method, status, dur)

		// Push to Loki using the exact same response line
		upID := lrw.Header().Get("X-Upstream")
		reqID := r.Header.Get("X-Request-ID")
		pushLoki("upstream", map[string]string{
			"method":     r.Method,
			"status":     strconv.Itoa(status),
			"upstream":   upID,
			"host":       mustHostname(),
			"request_id": reqID,
		}, respLine)
	})
}

// withRequestID assigns a unique ID to each request and includes it in the logs.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip logging/headers for Prometheus scrapes
		if isMetricsScrape(r) {
			next.ServeHTTP(w, r)
			return
		}

		reqID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&requestCounter, 1))
		r.Header.Set("X-Request-ID", reqID)
		preLine := fmt.Sprintf("REQ_ID=%s method=%s url=%s", reqID, r.Method, r.URL.Path)
		log.Print(preLine)
		// Push the exact same REQ_ID pre line to Loki
		pushLoki("upstream", map[string]string{
			"request_id": reqID,
			"method":     r.Method,
			"host":       mustHostname(),
		}, preLine)

		next.ServeHTTP(w, r)

		postLine := fmt.Sprintf("REQ_ID=%s completed", reqID)
		log.Print(postLine)
		// Push the exact same REQ_ID completion line to Loki
		pushLoki("upstream", map[string]string{
			"request_id": reqID,
			"method":     r.Method,
			"host":       mustHostname(),
		}, postLine)
	})
}

// parseCacheControl splits Cache-Control into normalized directives for logging.
func parseCacheControl(v string) []string {
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

// Identify Prometheus /metrics scrapes to reduce logging noise
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

// ------- Loki push (opt-in via env LOKI_URL) -------

var (
	lokiURL    string
	lokiOnce   sync.Once
	lokiClient = &http.Client{Timeout: 200 * time.Millisecond}
)

func initLoki() {
	lokiURL = ""
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
		}
		if b, err := os.ReadFile(cfgFile); err == nil {
			if err := yaml.Unmarshal(b, &cfg); err == nil {
				if cfg.Metrics != nil && strings.TrimSpace(cfg.Metrics.LokiURL) != "" {
					lokiURL = strings.TrimSpace(cfg.Metrics.LokiURL)
				}
			}
		}
	}

	// Accept values like "http://localhost:3100" or full push path
	if lokiURL != "" && !strings.Contains(lokiURL, "/loki/api/v1/push") {
		lokiURL = strings.TrimRight(lokiURL, "/") + "/loki/api/v1/push"
	}
}

func pushLoki(app string, labels map[string]string, line string) {
	lokiOnce.Do(initLoki)
	if lokiURL == "" {
		return // disabled until LOKI_URL is set
	}

	// Safe, low-cardinality labels to help Grafana drilldown from metrics
	lbls := map[string]string{
		"app": app,
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

func mustHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}