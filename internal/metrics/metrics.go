// Package metrics defines Prometheus metrics for both the proxy (edge) and the upstream (origin).
// It separates low-cardinality proxy metrics from per-upstream proxy metrics to avoid cardinality explosions.
// All helpers below encapsulate label normalization and consistent observation patterns.
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Proxy metrics (low-cardinality)
// These are intended to stay low-cardinality: avoid adding labels with many possible values.
var (
	// proxyRequestsTotal counts proxy responses by HTTP method, response status, and cache result.
	// Labels:
	// - method: HTTP method (GET/POST/...)
	// - status: numeric HTTP status (200/404/...)
	// - cache: cache outcome (HIT/MISS/BYPASS/...)
	// - url: the requested URL path (normalized to avoid high cardinality)
	proxyRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "proxy_requests_total",
			Help: "Total proxy responses by method, status and cache result",
		},
		[]string{"method", "status", "cache"},
	)
	// proxyReqDuration captures end-to-end proxy latency (client-facing).
	// Labels:
	// - method
	// - cache
	proxyReqDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "proxy_request_duration_seconds",
			Help:    "End-to-end proxy request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "cache"},
	)
	// proxyUpstreamInflight tracks in-flight requests per upstream host as seen by the proxy.
	// Label:
	// - upstream: upstream host or identifier; use stable, bounded values to limit cardinality.
	proxyUpstreamInflight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "proxy_upstream_inflight",
			Help: "Number of in-flight upstream requests by upstream host",
		},
		[]string{"upstream"},
	)
	// queueDepth reports the number of requests currently waiting in the proxy queue (not executing).
	queueDepth = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "proxy_queue_depth",
			Help: "Current queue depth (waiting only)",
		},
	)
	// queueRejected counts requests rejected because the queue was full.
	queueRejected = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "proxy_queue_rejected_total",
			Help: "Total requests rejected due to full queue",
		},
	)
	// queueTimeouts counts requests that timed out before leaving the queue.
	queueTimeouts = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "proxy_queue_timeouts_total",
			Help: "Total requests that timed out while waiting in queue",
		},
	)
	// queueWait measures time spent waiting in the queue (excludes execution time).
	queueWait = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "proxy_queue_wait_seconds",
			Help:    "Observed time spent waiting in the queue",
			Buckets: prometheus.DefBuckets,
		},
	)
)

// New: per-upstream (X-Upstream) proxy-side metrics
// These metrics attribute proxy-observed behavior to a specific upstream (e.g., from an X-Upstream header).
// Keep the "upstream" label bounded to avoid high cardinality (service names, not dynamic IDs/hosts where possible).
var (
	// proxyUpstreamRequestsTotal counts upstream responses as observed by the proxy.
	// Labels:
	// - upstream: logical upstream identifier (or "unknown" if not provided)
	// - method
	// - status
	proxyUpstreamRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "proxy_upstream_requests_total",
			Help: "Total upstream responses observed by the proxy, labeled by upstream (X-Upstream), method and status",
		},
		[]string{"upstream", "method", "status"},
	)
	// proxyUpstreamReqDuration measures upstream request duration from the proxy's perspective.
	// Labels:
	// - upstream
	// - method
	proxyUpstreamReqDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "proxy_upstream_request_duration_seconds",
			Help:    "Upstream request duration observed at the proxy by upstream and method",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"upstream", "method"},
	)
)

// Upstream metrics
// These should be emitted by the upstream service itself (origin), not the proxy.
var (
	// upRequestsTotal counts requests handled by the upstream service by method and status.
	upRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "upstream_requests_total",
			Help: "Total upstream responses by method and status",
		},
		[]string{"method", "status"},
	)
	// upRequestDuration measures upstream handler latency (server-side).
	upRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "upstream_request_duration_seconds",
			Help:    "Upstream request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method"},
	)
	// upInflight tracks concurrent requests currently executing in the upstream.
	upInflight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "upstream_inflight",
			Help: "Number of in-flight requests in upstream server",
		},
	)
)

func init() {
	// Register all metrics with the default Prometheus registry.
	// MustRegister will panic on programmer errors (e.g., duplicate registration).
	prometheus.MustRegister(
		// proxy
		proxyRequestsTotal,
		proxyReqDuration,
		proxyUpstreamInflight,
		queueDepth,
		queueRejected,
		queueTimeouts,
		queueWait,
		// upstream
		upRequestsTotal,
		upRequestDuration,
		upInflight,
		// New: proxy-side per-upstream
		proxyUpstreamRequestsTotal,
		proxyUpstreamReqDuration,
	)
}

// normCacheLabel normalizes the cache label to a bounded set of values.
// Empty cache outcomes are reported as "BYPASS" to avoid an empty label value.
func normCacheLabel(v string) string {
	if v == "" {
		return "BYPASS"
	}
	return v
}

// ---- Proxy helpers ----

// ObserveProxyResponse records a client-facing proxy response.
// Ensures low-cardinality labels by using method, numeric status, and normalized cache outcome.
func ObserveProxyResponse(method string, status int, cache string, dur time.Duration) {
	cache = normCacheLabel(cache)
	proxyRequestsTotal.WithLabelValues(method, strconv.Itoa(status), cache).Inc()
	proxyReqDuration.WithLabelValues(method, cache).Observe(dur.Seconds())
}

// ObserveProxyUpstreamResponse records the upstream response as seen by the proxy.
// If the upstream identifier is missing, "unknown" is used to keep label completeness consistent.
func ObserveProxyUpstreamResponse(upstream, method string, status int, dur time.Duration) {
	if upstream == "" {
		upstream = "unknown"
	}
	proxyUpstreamRequestsTotal.WithLabelValues(upstream, method, strconv.Itoa(status)).Inc()
	proxyUpstreamReqDuration.WithLabelValues(upstream, method).Observe(dur.Seconds())
}

// IncProxyUpstreamInflight increments the in-flight counter for a given upstream host.
// Pair with DecProxyUpstreamInflight to avoid leaks.
func IncProxyUpstreamInflight(host string) { proxyUpstreamInflight.WithLabelValues(host).Inc() }

// DecProxyUpstreamInflight decrements the in-flight counter for a given upstream host.
func DecProxyUpstreamInflight(host string) { proxyUpstreamInflight.WithLabelValues(host).Dec() }

// QueueRejectedInc increments the count of requests rejected due to a full queue.
func QueueRejectedInc() { queueRejected.Inc() }

// QueueTimeoutsInc increments the count of requests that timed out while waiting in the queue.
func QueueTimeoutsInc() { queueTimeouts.Inc() }

// QueueWaitObserve observes time spent waiting in the queue for a single request.
func QueueWaitObserve(d time.Duration) { queueWait.Observe(d.Seconds()) }

// QueueDepthSet sets the current queue depth (waiting requests only).
func QueueDepthSet(depth int64) { queueDepth.Set(float64(depth)) }

// ---- Upstream helpers ----

// UpstreamInflightInc increments the number of in-flight requests in the upstream.
func UpstreamInflightInc() { upInflight.Inc() }

// UpstreamInflightDec decrements the number of in-flight requests in the upstream.
func UpstreamInflightDec() { upInflight.Dec() }

// ObserveUpstreamResponse records an upstream (origin) response with method and status and observes duration.
func ObserveUpstreamResponse(method string, status int, dur time.Duration) {
	upRequestsTotal.WithLabelValues(method, strconv.Itoa(status)).Inc()
	upRequestDuration.WithLabelValues(method).Observe(dur.Seconds())
}
