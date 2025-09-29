package proxy

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type Balancer interface {
	// Pick selects a target. If preview=true it MUST NOT alter active connection
	// counters (used for cache key pre-selection).
	Pick(preview bool) *url.URL
	// Acquire marks a real upstream request start for the chosen target and returns
	// a release func to call when done.
	Acquire(t *url.URL) func()
	Targets() []*url.URL
	Strategy() string
}

// ----- Round Robin -----

type roundRobinBalancer struct {
	targets            []*url.URL
	idx                uint64
	healthChecksEnabled bool
}

func NewRoundRobinBalancer(ts []*url.URL, healthChecksEnabled bool) Balancer {
	cp := append([]*url.URL{}, ts...)
	return &roundRobinBalancer{targets: cp, healthChecksEnabled: healthChecksEnabled}
}

func (b *roundRobinBalancer) Pick(preview bool) *url.URL {
	if len(b.targets) == 0 {
		return nil
	}

	// On preview, preserve original behavior (no health probe).
	if preview {
		n := atomic.LoadUint64(&b.idx)
		return b.targets[n%uint64(len(b.targets))]
	}

	// Advance RR pointer
	start := atomic.AddUint64(&b.idx, 1) - 1
	l := uint64(len(b.targets))
	// If health checks are disabled, return the next target in pure RR.
	if !b.healthChecksEnabled {
		return b.targets[start%l]
	}

	// Health checks enabled: scan for the first healthy target in RR order.
	for i := uint64(0); i < l; i++ {
		cand := b.targets[(start+i)%l]
		if isTargetHealthy(cand) {
			return cand
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
	u      *url.URL
	active int64
}

type leastConnectionsBalancer struct {
	states              []*lcState
	healthChecksEnabled bool
}

func NewLeastConnectionsBalancer(ts []*url.URL, healthChecksEnabled bool) Balancer {
	states := make([]*lcState, 0, len(ts))
	for _, u := range ts {
		states = append(states, &lcState{u: u})
	}
	return &leastConnectionsBalancer{states: states, healthChecksEnabled: healthChecksEnabled}
}

func (b *leastConnectionsBalancer) Pick(preview bool) *url.URL {
	if len(b.states) == 0 {
		return nil
	}

	// Build a snapshot ordered by active connections (ascending, stable).
	type snap struct {
		u      *url.URL
		active int64
	}
	snaps := make([]snap, 0, len(b.states))
	for _, st := range b.states {
		snaps = append(snaps, snap{u: st.u, active: atomic.LoadInt64(&st.active)})
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].active < snaps[j].active })

	// On preview, return best by strategy without probing.
	if preview {
		return snaps[0].u
	}

	// If health checks are disabled, pick purely by LC order.
	if !b.healthChecksEnabled {
		return snaps[0].u
	}

	// Health checks enabled: choose the first healthy according to LC order.
	for _, s := range snaps {
		if isTargetHealthy(s.u) {
			return s.u
		}
	}
	return nil
}

func (b *leastConnectionsBalancer) Acquire(t *url.URL) func() {
	var target *lcState
	for _, st := range b.states {
		if st.u == t {
			target = st
			break
		}
	}
	if target == nil {
		return func() {}
	}
	atomic.AddInt64(&target.active, 1)
	return func() {
		atomic.AddInt64(&target.active, -1)
	}
}

func (b *leastConnectionsBalancer) Targets() []*url.URL {
	out := make([]*url.URL, 0, len(b.states))
	for _, st := range b.states {
		out = append(out, st.u)
	}
	return out
}
func (b *leastConnectionsBalancer) Strategy() string { return "least_connections" }

// ----- Factory / Configuration -----

func newBalancer(strategy string, targets []*url.URL, healthChecksEnabled bool) Balancer {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "least_conn", "lc", "least-connections", "least_connections":
		return NewLeastConnectionsBalancer(targets, healthChecksEnabled)
	default:
		return NewRoundRobinBalancer(targets, healthChecksEnabled)
	}
}

// ----- On-demand health check -----

var healthHTTPClient = &http.Client{
	Timeout: 500 * time.Millisecond,
}

func isTargetHealthy(u *url.URL) bool {
	// Build absolute health URL at root (/healthz).
	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	healthURL := &url.URL{
		Scheme: scheme,
		Host:   u.Host,
		Path:   "/healthz",
	}
	req, err := http.NewRequest("GET", healthURL.String(), nil)
	if err != nil {
		return false
	}
	// Hint to avoid connection reuse issues on failing endpoints.
	req.Close = true

	resp, err := healthHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Consider 2xx/3xx as healthy.
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}
