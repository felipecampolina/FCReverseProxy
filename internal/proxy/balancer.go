package proxy

import (
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
)

type Balancer interface {
	// Pick selects an upstream target.
	// If previewOnly is true, it MUST NOT mutate any state (e.g., active connection counters).
	Pick(previewOnly bool) *url.URL
	// Acquire marks the start of a real upstream request for targetURL and returns a release function.
	// Call the returned function when the request finishes to properly decrement counters.
	Acquire(targetURL *url.URL) func()
	// Targets returns the current list of upstream targets.
	Targets() []*url.URL
	// Strategy returns the name of the balancing strategy.
	Strategy() string
}

// ----- Round Robin -----

type roundRobinBalancer struct {
	targets             []*url.URL // immutable list of upstream targets
	nextIndex           uint64     // next index to use for round-robin (atomic)
	healthChecksEnabled bool       // whether on-demand health probes are used
}

func NewRoundRobinBalancer(upstreamTargets []*url.URL, healthChecksEnabled bool) Balancer {
	// Defensive copy to avoid accidental external mutations.
	copiedTargets := append([]*url.URL{}, upstreamTargets...)
	return &roundRobinBalancer{targets: copiedTargets, healthChecksEnabled: healthChecksEnabled}
}

func (b *roundRobinBalancer) Pick(previewOnly bool) *url.URL {
	if len(b.targets) == 0 {
		return nil
	}

	// On preview, do not advance the pointer or probe health.
	if previewOnly {
		n := atomic.LoadUint64(&b.nextIndex)
		return b.targets[n%uint64(len(b.targets))]
	}

	// Advance RR pointer once for this selection.
	startIndex := atomic.AddUint64(&b.nextIndex, 1) - 1
	targetCount := uint64(len(b.targets))

	// If health checks are disabled, select purely by RR order.
	if !b.healthChecksEnabled {
		return b.targets[startIndex%targetCount]
	}

	// Health checks enabled: return the first healthy target in RR order.
	for i := uint64(0); i < targetCount; i++ {
		candidateTarget := b.targets[(startIndex+i)%targetCount]
		if isTargetHealthy(candidateTarget) {
			return candidateTarget
		}
	}
	// None are healthy.
	return nil
}

func (b *roundRobinBalancer) Acquire(_ *url.URL) func() { return func() {} } 
func (b *roundRobinBalancer) Targets() []*url.URL       { return b.targets }
func (b *roundRobinBalancer) Strategy() string          { return "round_robin" }

// ----- Least Connections -----

type lcState struct {
	upstreamURL       *url.URL // upstream target URL
	activeConnections int64    // number of in-flight requests (atomic)
}

type leastConnectionsBalancer struct {
	targetStates        []*lcState
	healthChecksEnabled bool
}

func NewLeastConnectionsBalancer(upstreamTargets []*url.URL, healthChecksEnabled bool) Balancer {
	// Initialize state for each target.
	targetStates := make([]*lcState, 0, len(upstreamTargets))
	for _, u := range upstreamTargets {
		targetStates = append(targetStates, &lcState{upstreamURL: u})
	}
	return &leastConnectionsBalancer{targetStates: targetStates, healthChecksEnabled: healthChecksEnabled}
}

func (b *leastConnectionsBalancer) Pick(previewOnly bool) *url.URL {
	if len(b.targetStates) == 0 {
		return nil
	}

	// Build a snapshot ordered by active connections (ascending, stable).
	type snapshotEntry struct {
		targetURL         *url.URL
		activeConnections int64
	}
	snapshot := make([]snapshotEntry, 0, len(b.targetStates))
	for _, st := range b.targetStates {
		snapshot = append(snapshot, snapshotEntry{
			targetURL:         st.upstreamURL,
			activeConnections: atomic.LoadInt64(&st.activeConnections),
		})
	}
	sort.SliceStable(snapshot, func(i, j int) bool { return snapshot[i].activeConnections < snapshot[j].activeConnections })

	// On preview, return best by strategy without probing.
	if previewOnly {
		return snapshot[0].targetURL
	}

	// If health checks are disabled, pick purely by LC order.
	if !b.healthChecksEnabled {
		return snapshot[0].targetURL
	}

	// Health checks enabled: choose the first healthy according to LC order.
	for _, entry := range snapshot {
		if isTargetHealthy(entry.targetURL) {
			return entry.targetURL
		}
	}
	return nil
}

func (b *leastConnectionsBalancer) Acquire(targetURL *url.URL) func() {
	var selectedState *lcState
	for _, st := range b.targetStates {
		if st.upstreamURL == targetURL {
			selectedState = st
			break
		}
	}
	if selectedState == nil {
		return func() {}
	}
	atomic.AddInt64(&selectedState.activeConnections, 1)
	return func() {
		atomic.AddInt64(&selectedState.activeConnections, -1)
	}
}

func (b *leastConnectionsBalancer) Targets() []*url.URL {
	out := make([]*url.URL, 0, len(b.targetStates))
	for _, st := range b.targetStates {
		out = append(out, st.upstreamURL)
	}
	return out
}
func (b *leastConnectionsBalancer) Strategy() string { return "least_connections" }


// newBalancer creates a Balancer based on the specified strategy.
func newBalancer(strategy string, upstreamTargets []*url.URL, healthChecksEnabled bool) Balancer {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "least_conn", "lc", "least-connections", "least_connections":
		return NewLeastConnectionsBalancer(upstreamTargets, healthChecksEnabled)
	default:
		return NewRoundRobinBalancer(upstreamTargets, healthChecksEnabled)
	}
}

// ConfigureBalancer switches balancing strategy at runtime.
func (proxy *ReverseProxy) ConfigureBalancer(strategy string) {
	proxy.lbStrategy = strategy
	proxy.balancer = newBalancer(proxy.lbStrategy, proxy.targets, proxy.healthChecksEnabled)
}

// Toggle active health checks in the load balancer at runtime.
func (proxy *ReverseProxy) SetHealthCheckEnabled(enabled bool) {
	proxy.healthChecksEnabled = enabled
	proxy.balancer = newBalancer(proxy.lbStrategy, proxy.targets, proxy.healthChecksEnabled)
}

