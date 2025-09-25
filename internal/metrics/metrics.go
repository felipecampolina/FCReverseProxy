package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Proxy metrics (low-cardinality)
var (
	proxyRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "proxy_requests_total",
			Help: "Total proxy responses by method, status and cache result",
		},
		[]string{"method", "status", "cache"},
	)
	proxyReqDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "proxy_request_duration_seconds",
			Help:    "End-to-end proxy request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "cache"},
	)
	proxyUpstreamInflight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "proxy_upstream_inflight",
			Help: "Number of in-flight upstream requests by upstream host",
		},
		[]string{"upstream"},
	)
	queueDepth = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "proxy_queue_depth",
			Help: "Current queue depth (waiting only)",
		},
	)
	queueRejected = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "proxy_queue_rejected_total",
			Help: "Total requests rejected due to full queue",
		},
	)
	queueTimeouts = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "proxy_queue_timeouts_total",
			Help: "Total requests that timed out while waiting in queue",
		},
	)
	queueWait = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "proxy_queue_wait_seconds",
			Help:    "Observed time spent waiting in the queue",
			Buckets: prometheus.DefBuckets,
		},
	)
)

// Upstream metrics
var (
	upRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "upstream_requests_total",
			Help: "Total upstream responses by method and status",
		},
		[]string{"method", "status"},
	)
	upRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "upstream_request_duration_seconds",
			Help:    "Upstream request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method"},
	)
	upInflight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "upstream_inflight",
			Help: "Number of in-flight requests in upstream server",
		},
	)
)

func init() {
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
	)
}

func normCacheLabel(v string) string {
	if v == "" {
		return "BYPASS"
	}
	return v
}

// ---- Proxy helpers ----

func ObserveProxyResponse(method string, status int, cache string, dur time.Duration) {
	cache = normCacheLabel(cache)
	proxyRequestsTotal.WithLabelValues(method, strconv.Itoa(status), cache).Inc()
	proxyReqDuration.WithLabelValues(method, cache).Observe(dur.Seconds())
}

func IncProxyUpstreamInflight(host string) { proxyUpstreamInflight.WithLabelValues(host).Inc() }
func DecProxyUpstreamInflight(host string) { proxyUpstreamInflight.WithLabelValues(host).Dec() }

func QueueRejectedInc()                        { queueRejected.Inc() }
func QueueTimeoutsInc()                        { queueTimeouts.Inc() }
func QueueWaitObserve(d time.Duration)         { queueWait.Observe(d.Seconds()) }
func QueueDepthSet(depth int64)                { queueDepth.Set(float64(depth)) }

// ---- Upstream helpers ----

func UpstreamInflightInc()                     { upInflight.Inc() }
func UpstreamInflightDec()                     { upInflight.Dec() }
func ObserveUpstreamResponse(method string, status int, dur time.Duration) {
	upRequestsTotal.WithLabelValues(method, strconv.Itoa(status)).Inc()
	upRequestDuration.WithLabelValues(method).Observe(dur.Seconds())
}
