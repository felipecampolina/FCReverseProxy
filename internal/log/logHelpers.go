package applog

import (
	"bytes"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	// switched to v3 to match Makefile deps
	"gopkg.in/yaml.v3"
)

// logEnabled reports whether local log printing should run.
// It disables local log printing for unit test .
func logEnabled() bool {
	// In test binaries, the testing package registers these flags.
	if flag.Lookup("test.v") != nil || flag.Lookup("test.run") != nil || flag.Lookup("test.bench") != nil {
		return false
	}
	return true
}

// parseCacheControlList parses a Cache-Control style header into a normalized list of directives.
// Example: "no-cache, max-age=60" -> ["no-cache", "max-age=60"]
func parseCacheControlList(headerValue string) []string {
	if headerValue == "" {
		return nil
	}
	rawParts := strings.Split(headerValue, ",")
	directives := make([]string, 0, len(rawParts))
	for _, raw := range rawParts {
		part := strings.TrimSpace(strings.ToLower(raw))
		if part == "" {
			continue
		}
		directives = append(directives, part)
	}
	return directives
}

// isMetricsScrape tries to detect Prometheus/OpenMetrics scrapes.
func isMetricsScrape(req *http.Request) bool {
	if req.URL != nil && req.URL.Path == "/metrics" {
		return true
	}
	if strings.Contains(req.Header.Get("User-Agent"), "Prometheus") {
		return true
	}
	if strings.Contains(req.Header.Get("Accept"), "openmetrics") {
		return true
	}
	return false
}

// MustHostname returns the current hostname or "unknown" on error.
func MustHostname() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return "unknown"
	}
	return hostname
}

// Emit prints locally (if enabled and level allowed) and pushes the same line to Loki.
// The "level" is normalized (lowercased) and also used to filter based on config.
func Emit(level, app string, labels map[string]string, line string) {
	normalizedLevel := strings.ToLower(level)

	// Local print (skip during tests)
	if logEnabled() && levelEnabled(normalizedLevel) {
		log.Print(line)
	}

	// Forward to Loki with the "level" label applied
	PushLokiWithLevel(normalizedLevel, app, labels, line)
}

// levelEnabled reports if a given log level is enabled according to config.
// The following package-level toggles are read here (defined elsewhere):
//   - infoEnabled
//   - debugEnabled
//   - errorEnabled
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

// PushLokiWithLevel sends a single log line with labels to Loki, adding a "level" label.
// It is a no-op if Loki is not configured or if the provided level is disabled.
func PushLokiWithLevel(level, app string, labels map[string]string, line string) {
	lokiOnce.Do(initLoki)
	if lokiURL == "" || !levelEnabled(level) {
		return
	}

	// Prepare stream labels (always include "app" and "level")
	streamLabels := map[string]string{
		"app":   app,
		"level": strings.ToLower(strings.TrimSpace(level)),
	}
	for k, v := range labels {
		if strings.TrimSpace(k) == "" {
			continue
		}
		streamLabels[k] = v
	}

	// Loki expects timestamps in nanoseconds since epoch as string
	timestampNanos := strconv.FormatInt(time.Now().UnixNano(), 10)

	// Minimal Loki push payload: one stream with one value (timestamp + line)
	lokiPayload := struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"streams"`
	}{
		Streams: []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		}{
			{Stream: streamLabels, Values: [][2]string{{timestampNanos, line}}},
		},
	}

	payloadBytes, _ := json.Marshal(lokiPayload)

	// Fire-and-forget HTTP request
	request, err := http.NewRequest("POST", lokiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	_, _ = lokiClient.Do(request)
}

// initLoki lazily reads configuration for Loki URL and logging level toggles.
// Precedence:
//   1) If configs/config.yaml or configs/config.yml exists, read them.
//   2) If loki_url is a base URL, normalize it to the push endpoint:
//      <base>/loki/api/v1/push
func initLoki() {
	// Default: not configured
	lokiURL = ""

	// Prefer configs/config.yaml|yml
	configPath := ""
	for _, candidatePath := range []string{"configs/config.yaml", "configs/config.yml"} {
		if _, err := os.Stat(candidatePath); err == nil {
			configPath = candidatePath
			break
		}
	}

	if configPath != "" {
		var config struct {
			Metrics *struct {
				LokiURL string `yaml:"loki_url"`
			} `yaml:"metrics"`
			Logging *struct {
				InfoEnabled  *bool `yaml:"info_enabled"`
				DebugEnabled *bool `yaml:"debug_enabled"`
				ErrorEnabled *bool `yaml:"error_enabled"`
			} `yaml:"logging"`
		}

		if cfgBytes, err := os.ReadFile(configPath); err == nil {
			if err := yaml.Unmarshal(cfgBytes, &config); err == nil {
				// Loki URL (may be base or full push path)
				if config.Metrics != nil && strings.TrimSpace(config.Metrics.LokiURL) != "" {
					lokiURL = strings.TrimSpace(config.Metrics.LokiURL)
				}
				// Apply logging level toggles if present
				if config.Logging != nil {
					if config.Logging.InfoEnabled != nil {
						infoEnabled = *config.Logging.InfoEnabled
					}
					if config.Logging.DebugEnabled != nil {
						debugEnabled = *config.Logging.DebugEnabled
					}
					if config.Logging.ErrorEnabled != nil {
						errorEnabled = *config.Logging.ErrorEnabled
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

