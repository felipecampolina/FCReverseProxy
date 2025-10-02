package proxy

import (
	"math"
	"net/url"
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
	pendingSelections int64    // in-flight reservations made by Pick (atomic)
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

	// Helper to compute minimal load and return candidates in stable order.
	// load is active + pending for non-preview; active only for preview.
	findCandidates := func(includePending bool) ([]*lcState, bool) {
		min := int64(math.MaxInt64)
		cands := make([]*lcState, 0, len(b.targetStates))
		for _, st := range b.targetStates {
			if b.healthChecksEnabled && !isTargetHealthy(st.upstreamURL) {
				continue
			}
			load := atomic.LoadInt64(&st.activeConnections)
			if includePending {
				load += atomic.LoadInt64(&st.pendingSelections)
			}
			if load < min {
				min = load
				cands = cands[:0]
				cands = append(cands, st)
			} else if load == min {
				cands = append(cands, st)
			}
		}
		// when health checks are enabled, nil if no healthy targets
		return cands, len(cands) > 0
	}

	// Preview: no mutation, stable tie-breaker.
	if previewOnly {
		if cands, ok := findCandidates(false); ok {
			return cands[0].upstreamURL
		}
		return nil
	}

	// Non-preview: reserve a slot to avoid double-pick under concurrency.
	for {
		cands, ok := findCandidates(true)
		if !ok {
			// If health checks disabled and we somehow got none, re-scan without health filter.
			if !b.healthChecksEnabled {
				for _, st := range b.targetStates {
					// fallback to first in stable order
					return st.upstreamURL
				}
			}
			return nil
		}
		best := cands[0]
		// Try to reserve: CAS pendingSelections = p -> p+1
		p := atomic.LoadInt64(&best.pendingSelections)
		if atomic.CompareAndSwapInt64(&best.pendingSelections, p, p+1) {
			return best.upstreamURL
		}
		// Contention detected; retry selection with updated loads.
	}
}

func (b *leastConnectionsBalancer) Acquire(targetURL *url.URL) func() {
	var selectedState *lcState
	for _, st := range b.targetStates {
		if sameUpstream(st.upstreamURL, targetURL) {
			selectedState = st
			break
		}
	}
	if selectedState == nil {
		return func() {}
	}
	// Convert reservation into an active connection.
	atomic.AddInt64(&selectedState.pendingSelections, -1)
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

// sameUpstream compares two URLs as upstream identities (scheme + host + normalized port).
func sameUpstream(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	sa := strings.ToLower(a.Scheme)
	sb := strings.ToLower(b.Scheme)

	ha := strings.ToLower(a.Hostname())
	hb := strings.ToLower(b.Hostname())

	pa := a.Port()
	pb := b.Port()
	if pa == "" {
		switch sa {
		case "http":
			pa = "80"
		case "https":
			pa = "443"
		}
	}
	if pb == "" {
		switch sb {
		case "http":
			pb = "80"
		case "https":
			pb = "443"
		}
	}

	return sa == sb && ha == hb && pa == pb
}

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
