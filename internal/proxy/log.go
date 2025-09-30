package proxy

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	imetrics "traefik-challenge-2/internal/metrics"

	"gopkg.in/yaml.v3"
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
	line := fmt.Sprintf(
		"REQ remote=%s method=%s url=%s proto=%s req-content-length=%s headers=%v | CACHE HIT",
		r.RemoteAddr,
		r.Method,
		r.URL.RequestURI(),
		r.Proto,
		r.Header.Get("Content-Length"),
		r.Header,
	)
	if logEnabled() {
		log.Print(line)
	}

	// Push to Loki (exact same line as local log)
	pushLoki("proxy", map[string]string{
		"method": r.Method,
		"status": "200", // cache hit implies we returned the cached status; exact code is logged in response path
		"cache":  "HIT",
		"host":   mustHostname(),
	}, line)
}

// LogResponse logs details of a response in the specified pattern.
// Also records Prometheus metrics using response headers (including X-Cache).
func logResponseCacheHit(status int, bytes int, dur time.Duration, respHeaders http.Header, req *http.Request, resp http.ResponseWriter, notModified bool, respBodyNote string) {
	line := fmt.Sprintf(
		"RESP status=%d bytes=%d dur=%s resp-content-length=%s resp_headers=%v | cache:req_cc=%v resp_cc=%v etag=%q last-modified=%q expires=%q age=%q via=%q x-cache=%q not-modified=%t%s",
		status,
		bytes,
		dur.String(),
		respHeaders.Get("Content-Length"),
		respHeaders,
		parseCacheControl(req.Header.Get("Cache-Control")),
		parseCacheControl(respHeaders.Get("Cache-Control")),
		respHeaders.Get("ETag"),
		respHeaders.Get("Last-Modified"),
		respHeaders.Get("Expires"),
		respHeaders.Get("Age"),
		respHeaders.Get("Via"),
		respHeaders.Get("X-Cache"),
		notModified,
		respBodyNote,
	)
	if logEnabled() {
		log.Print(line)
	}

	cacheLabel := respHeaders.Get("X-Cache")
	imetrics.ObserveProxyResponse(req.Method, status, cacheLabel, dur)

	// Push to Loki (exact same line as local log)
	up := respHeaders.Get("X-Upstream")
	if strings.TrimSpace(up) == "" {
		up = "unknown"
	}
	pushLoki("proxy", map[string]string{
		"method":   req.Method,
		"status":   strconv.Itoa(status),
		"cache":    cacheLabel,
		"upstream": up,
		"host":     mustHostname(),
	}, line)
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
		return
	}

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


